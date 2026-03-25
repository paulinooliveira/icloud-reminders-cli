#!/usr/bin/env python3
"""iCloud Reminders CLI -- pyicloud for auth, CloudKit (ckdatabasews) for data."""

import argparse
import base64
import gzip
import zlib
import io
import json
import re
import sys
import time
import uuid
from datetime import datetime
from pathlib import Path

CONFIG_DIR = Path.home() / ".config" / "icloud-reminders" / "pyicloud_session"
CREDS_FILE  = Path.home() / ".config" / "icloud-reminders" / "credentials.json"
CK_PATH     = "/database/1/com.apple.reminders/production/private"
UUID_RE     = re.compile(r"[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}")
UUID_RE_B   = re.compile(rb"[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}")

PRIORITY_MAP   = {"high": 1, "medium": 5, "low": 9, "none": 0}
PRIORITY_LABEL = {1: "high", 5: "medium", 9: "low", 0: "none"}

def bare_id(record_name):
    """Strip any Reminder/ prefix — bare UUID is the only canonical form."""
    if not record_name:
        return record_name
    return record_name.split("/")[-1].upper()





def err(msg):
    print(msg, file=sys.stderr)

def die(msg, code=1):
    err(msg)
    sys.exit(code)

def load_creds():
    if CREDS_FILE.exists():
        with open(CREDS_FILE) as f:
            return json.load(f)
    return {}

def save_creds(apple_id, password):
    CREDS_FILE.parent.mkdir(parents=True, exist_ok=True)
    with open(CREDS_FILE, "w") as f:
        json.dump({"apple_id": apple_id, "password": password}, f)
    CREDS_FILE.chmod(0o600)




def get_api(prompt_creds=False):
    from pyicloud import PyiCloudService
    from pyicloud.exceptions import PyiCloudFailedLoginException

    creds = load_creds()
    apple_id = creds.get("apple_id")
    password  = creds.get("password")

    if prompt_creds or not apple_id:
        apple_id = input("Apple ID: ").strip()
        password  = input("Password: ").strip()
        save_creds(apple_id, password)

    CONFIG_DIR.mkdir(parents=True, exist_ok=True)
    try:
        api = PyiCloudService(apple_id, password, cookie_directory=str(CONFIG_DIR))
    except PyiCloudFailedLoginException as e:
        die(f"Login failed: {e}")

    if api.requires_2fa:
        print("2FA required. Enter the 6-digit code sent to your device:")
        code = input("Code: ").strip()
        if not api.validate_2fa_code(code):
            die("2FA validation failed.")
        if not api.is_trusted_session:
            print("Trusting session...")
            api.trust_session()

    return api




def ck_url(api):
    return api._webservices["ckdatabasews"]["url"] + CK_PATH

def ck_params(api):
    return {**api.params, "getCurrentSyncToken": "true", "remapEnums": "true"}

def ck_post(api, path, body):
    resp = api.session.post(f"{ck_url(api)}/{path}", params=ck_params(api), json=body)
    resp.raise_for_status()
    return resp.json()

def get_owner(api):
    """Return ownerRecordName from zones/list."""
    data = ck_post(api, "zones/list", {})
    for z in data.get("zones", []):
        owner = z.get("zoneID", {}).get("ownerRecordName", "")
        if owner and owner != "currentUser":
            return owner
    die("Could not determine ownerRecordName from zones/list")

def zone_id(owner):
    return {"zoneName": "Reminders", "ownerRecordName": owner}




def _varint(v):
    buf = []
    while v > 127:
        buf.append((v & 0x7F) | 0x80)
        v >>= 7
    buf.append(v)
    return bytes(buf)

def _field(num, wire, data):
    tag = bytes([(num << 3) | wire])
    if wire == 0:
        return tag + _varint(int(data))
    if wire == 2:
        b = data if isinstance(data, (bytes, bytearray)) else bytes(data)
        return tag + _varint(len(b)) + b
    raise ValueError(f"unsupported wire type {wire}")

def _position(replica, offset):
    """CRDT position message; offset=-1 encodes sentinel 0xFFFFFFFF."""
    r = _field(1, 0, replica)
    if offset == -1:
        # sentinel: field 2 varint tag + varint(0xFFFFFFFF)
        o = bytes([(2 << 3) | 0]) + _varint(0xFFFFFFFF)
    else:
        o = _field(2, 0, offset)
    return r + o

def _read_varint(buf, pos):
    result, shift = 0, 0
    while pos < len(buf):
        b = buf[pos]; pos += 1
        result |= (b & 0x7F) << shift
        if not (b & 0x80):
            return result, pos
        shift += 7
    raise ValueError("truncated varint")

def _extract_ld_fields(buf, target_field):
    """Return list of byte-strings for all length-delimited occurrences of target_field."""
    out, pos = [], 0
    while pos < len(buf):
        tag, pos = _read_varint(buf, pos)
        fn, wt = tag >> 3, tag & 0x7
        if wt == 0:
            _, pos = _read_varint(buf, pos)
        elif wt == 2:
            length, pos = _read_varint(buf, pos)
            chunk = buf[pos:pos + length]; pos += length
            if fn == target_field:
                out.append(chunk)
        else:
            break
    return out




def extract_doc_uuid(td_b64):
    """Extract the 16-byte document UUID from an existing TitleDocument."""
    if not td_b64:
        return None
    try:
        compressed = base64.b64decode(td_b64)
        if compressed[:2] == b"\x1f\x8b":
            raw = gzip.decompress(compressed)
        else:
            raw = zlib.decompress(compressed)
        # Navigate: outer.field2(document).field3(note).field4(metadata).field1(uuid_entry).field1(uuid)
        docs = _extract_field(raw, 2)
        if not docs: return None
        notes = _extract_field(docs[0], 3)
        if not notes: return None
        metas = _extract_field(notes[0], 4)
        if not metas: return None
        entries = _extract_field(metas[0], 1)
        if not entries: return None
        uuids = _extract_field(entries[0], 1)
        if not uuids or len(uuids[0]) != 16: return None
        return uuids[0]
    except Exception:
        return None

def _extract_field(data, target_field):
    """Extract all length-delimited (wire type 2) values for a given field number."""
    results = []
    i = 0
    while i < len(data):
        if i >= len(data): break
        # Read varint tag
        tag = 0; shift = 0
        while i < len(data):
            b = data[i]; i += 1
            tag |= (b & 0x7f) << shift; shift += 7
            if not (b & 0x80): break
        field_num = tag >> 3
        wire_type = tag & 0x07
        if wire_type == 0:  # varint
            while i < len(data):
                b = data[i]; i += 1
                if not (b & 0x80): break
        elif wire_type == 2:  # length-delimited
            length = 0; shift = 0
            while i < len(data):
                b = data[i]; i += 1
                length |= (b & 0x7f) << shift; shift += 7
                if not (b & 0x80): break
            if field_num == target_field:
                results.append(data[i:i+length])
            i += length
        elif wire_type == 5:  # 32-bit
            i += 4
        elif wire_type == 1:  # 64-bit
            i += 8
        else:
            break
    return results


def encode_title(text, doc_uuid=None):
    """Encode text as Apple CRDT TitleDocument: protobuf -> gzip -> base64."""
    text_bytes = text.encode("utf-8")
    char_len   = len(text)          # CHARACTER count, not byte count

    if doc_uuid is None or len(doc_uuid) != 16:
        doc_uuid = uuid.uuid4().bytes

    # Op 1: initial position marker
    op1  = _field(1, 2, _position(0, 0))
    op1 += _field(2, 0, 0)
    op1 += _field(3, 2, _position(0, 0))
    op1 += _field(5, 0, 1)

    # Op 2: content insert (carries char_len)
    op2  = _field(1, 2, _position(1, 0))
    op2 += _field(2, 0, char_len)
    op2 += _field(3, 2, _position(1, 0))
    op2 += _field(5, 0, 2)

    # Op 3: sentinel end marker (no field 5)
    op3  = _field(1, 2, _position(0, -1))
    op3 += _field(2, 0, 0)
    op3 += _field(3, 2, _position(0, -1))

    # Metadata: uuid_entry { uuid, clock_payload, replica_payload }
    uuid_entry  = _field(1, 2, doc_uuid)
    uuid_entry += _field(2, 2, _field(1, 0, char_len))  # clock
    uuid_entry += _field(2, 2, _field(1, 0, 1))         # replica
    metadata = _field(1, 2, uuid_entry)

    # Note (field 3 in Document): text + ops + metadata + attrRun
    note  = _field(2, 2, text_bytes)
    note += _field(3, 2, op1)
    note += _field(3, 2, op2)
    note += _field(3, 2, op3)
    note += _field(4, 2, metadata)
    note += _field(5, 2, _field(1, 0, char_len))  # attributeRun

    # Document (field 2 in outer)
    document  = _field(1, 0, 0)
    document += _field(2, 0, 0)
    document += _field(3, 2, note)

    # Outer wrapper
    outer  = _field(1, 0, 0)
    outer += _field(2, 2, document)

    return base64.b64encode(zlib.compress(outer)).decode()

def extract_title(td_b64):
    """Decode a base64-gzip CRDT TitleDocument to plain text."""
    if not td_b64:
        return ""
    try:
        raw = base64.b64decode(td_b64)
        if raw[:2] == b"\x1f\x8b":
            raw = gzip.decompress(raw)
        elif raw[0] == 0x78:
            raw = zlib.decompress(raw)
        # outer -> field 2 (document) -> field 3 (note) -> field 2 (text bytes)
        docs = _extract_ld_fields(raw, 2)
        if docs:
            notes = _extract_ld_fields(docs[0], 3)
            if notes:
                texts = _extract_ld_fields(notes[0], 2)
                if texts:
                    return texts[0].decode("utf-8", errors="replace")
    except Exception:
        pass
    # Fallback: longest printable run in the decompressed blob
    try:
        raw2 = gzip.decompress(base64.b64decode(td_b64))
        matches = re.findall(rb"[\x20-\x7e\xc0-\xff]{3,}", raw2)
        if matches:
            return max(matches, key=len).decode("utf-8", errors="replace")
    except Exception:
        pass
    return "?"




def get_lists(api, owner):
    """Fetch all reminder lists via SearchIndexes + records/lookup."""
    # Step 1: query SearchIndexes(Account) to get list UUIDs from ordering field
    resp = ck_post(api, "records/query", {
        "query": {
            "recordType": "SearchIndexes",
            "filterBy": [{"comparator": "EQUALS", "fieldName": "indexName",
                          "fieldValue": {"value": "Account", "type": "STRING"}}],
        },
        "zoneID": zone_id(owner),
        "resultsLimit": 200,
    })
    uuids = []
    for rec in resp.get("records", []):
        for fname, fval in rec.get("fields", {}).items():
            if "ListIDsMergeableOrdering" in fname:
                raw = fval.get("value", "")
                # Field type is BYTES: base64-decode then scan for UUID strings
                if fval.get("type") == "BYTES":
                    try:
                        raw = base64.b64decode(raw)
                        uuids += [u.decode() for u in UUID_RE_B.findall(raw)]
                    except Exception:
                        pass
                else:
                    uuids += UUID_RE.findall(str(raw))
    uuids = list(dict.fromkeys(uuids))  # deduplicate, preserve order
    if not uuids:
        return []

    # Step 2: lookup each List/<UUID> record
    resp2 = ck_post(api, "records/lookup", {
        "records": [{"recordName": f"List/{u}"} for u in uuids],
        "zoneID": zone_id(owner),
    })
    result = []
    for rec in resp2.get("records", []):
        if rec.get("serverErrorCode") == "NOT_FOUND":
            continue
        rn   = rec.get("recordName", "")
        u    = rn.removeprefix("List/")
        fields_r = rec.get("fields", {})
        # List records use a plain "Name" field (not TitleDocument)
        name = (fields_r.get("Name") or fields_r.get("TitleDocument") or {}).get("value", "")
        if not isinstance(name, str) or not name:
            name = rn  # fallback to recordName
        elif fields_r.get("TitleDocument", {}).get("value"):
            name = extract_title(name)
        result.append({"name": name, "recordName": rn, "uuid": u})
    return result

def find_list(api, owner, list_name):
    for l in get_lists(api, owner):
        if l["name"].lower() == list_name.lower():
            return l
    die(f"List '{list_name}' not found")




def get_reminders(api, owner, list_uuid):
    # includeCompleted=1 means "fetch all" (server returns both states);
    # filter client-side so only incomplete items are shown.
    resp = ck_post(api, "records/query", {
        "query": {
            "recordType": "reminderList",
            "filterBy": [
                {"comparator": "EQUALS", "fieldName": "List",
                 "fieldValue": {"value": {"recordName": f"List/{list_uuid}", "action": "VALIDATE"},
                                "type": "REFERENCE"}},
                {"comparator": "EQUALS", "fieldName": "includeCompleted",
                 "fieldValue": {"value": 1, "type": "INT64"}},
            ],
        },
        "zoneID": zone_id(owner),
        "resultsLimit": 200,
    })
    records = resp.get("records", [])
    # Keep only non-deleted, non-completed reminders
    return [r for r in records
            if not r.get("fields", {}).get("Completed", {}).get("value", 0)
            and not r.get("fields", {}).get("Deleted", {}).get("value", 0)]

def fmt_reminder(rec):
    fields  = rec.get("fields", {})
    rn      = bare_id(rec.get("recordName", "?"))
    td      = fields.get("TitleDocument", {}).get("value", "")
    title   = extract_title(td) if td else fields.get("Title", {}).get("value", "(untitled)")
    prio    = fields.get("Priority", {}).get("value", 0)
    due_ms  = fields.get("DueDate", {}).get("value")
    flagged = fields.get("Flagged", {}).get("value", 0)

    parts = [f"  * {title}"]
    if flagged:
        parts.append("[flagged]")
    if prio and prio in PRIORITY_LABEL:
        parts.append(f"[{PRIORITY_LABEL[prio]}]")
    if due_ms:
        d = datetime.fromtimestamp(int(due_ms) / 1000)
        due_str = d.strftime("%Y-%m-%d") if (d.hour == 0 and d.minute == 0) else d.strftime("%Y-%m-%dT%H:%M")
        parts.append(f"[due {due_str}]")
    parts.append(f"({rn})")
    return "  ".join(parts)

def find_reminder_by_id(api, owner, record_name):
    # Try both bare UUID and Reminder/-prefixed forms
    bare = record_name.split('/')[-1]
    candidates = [f'Reminder/{bare}', bare]
    if record_name not in candidates:
        candidates.insert(0, record_name)
    resp = ck_post(api, 'records/lookup', {
        'records': [{'recordName': c} for c in candidates],
        'zoneID': zone_id(owner),
    })
    for rec in resp.get('records', []):
        if rec.get('serverErrorCode'):
            continue
        return rec
    die(f"Reminder '{record_name}' not found")




def cmd_auth(args, api):
    print(f"Authenticated as: {load_creds().get('apple_id', '?')}")

def cmd_sync(args, api):
    owner = get_owner(api)
    lists = get_lists(api, owner)
    total = sum(len(get_reminders(api, owner, l["uuid"])) for l in lists)
    print(f"Lists: {len(lists)}, Reminders (incomplete): {total}")

def cmd_lists(args, api):
    owner = get_owner(api)
    lists = get_lists(api, owner)
    if args.json:
        print(json.dumps([{"name": l["name"], "recordName": l["recordName"]} for l in lists], indent=2))
        return
    for l in lists:
        print(f"  {l['name']}  ({l['recordName']})")

def cmd_list(args, api):
    owner = get_owner(api)
    if args.list:
        lst  = find_list(api, owner, args.list)
        rems = get_reminders(api, owner, lst["uuid"])
    else:
        lists = get_lists(api, owner)
        rems  = []
        for l in lists:
            rems += get_reminders(api, owner, l["uuid"])

    if args.json:
        print(json.dumps(rems, indent=2, default=str))
        return

    print(f"\nReminders: {len(rems)}\n")
    for r in rems:
        print(fmt_reminder(r))

def cmd_add(args, api):
    owner = get_owner(api)
    if args.list:
        lst = find_list(api, owner, args.list)
        list_uuid = lst["uuid"]
    else:
        lists = get_lists(api, owner)
        if not lists:
            die("No lists found")
        list_uuid = lists[0]["uuid"]

    fields = {
        "TitleDocument": {"value": encode_title(args.title)},
        "Completed":     {"value": 0},
        "List":          {"value": {"recordName": f"List/{list_uuid}", "action": "NONE"}, "type": "REFERENCE"},
    }
    if args.priority:
        fields["Priority"] = {"value": PRIORITY_MAP.get(args.priority, 0)}
    if args.notes:
        fields["NotesDocument"] = {"value": encode_title(args.notes)}
    if args.due:
        try:
            fmt = "%Y-%m-%dT%H:%M" if "T" in args.due else "%Y-%m-%d"
            d = datetime.strptime(args.due, fmt)
            fields["DueDate"] = {"value": int(d.timestamp() * 1000)}
        except ValueError as e:
            die(f"Invalid date: {e}")

    rec_name = str(uuid.uuid4()).upper()
    resp = ck_post(api, "records/modify", {
        "zoneID": zone_id(owner),
        "atomic": True,
        "operations": [{"operationType": "create", "record": {
            "recordType": "Reminder",
            "recordName": rec_name,
            "fields": fields,
        }}],
    })
    saved = bare_id(resp.get("records", [{}])[0].get("recordName", rec_name))
    print(f"Added: '{args.title}' ({saved})")

def _bump_resolution_map(fields, touched_keys):
    """Bump ResolutionTokenMap counters for changed fields, matching icloud.com behavior."""
    import time as _time
    raw = fields.get("ResolutionTokenMap", {}).get("value", "{}")
    try:
        rtm = json.loads(raw) if isinstance(raw, str) else {}
    except (json.JSONDecodeError, TypeError):
        rtm = {}
    m = rtm.get("map", {})
    now = _time.time() - 978307200  # Apple epoch offset
    for key in touched_keys + ["lastModifiedDate"]:
        entry = m.get(key, {})
        entry["counter"] = entry.get("counter", 0) + 1
        entry["modificationTime"] = now
        if "replicaID" not in entry:
            entry["replicaID"] = str(uuid.uuid4()).upper()
        m[key] = entry
    rtm["map"] = m
    return json.dumps(rtm)


def replace_crdt_text(existing_td_b64, new_text):
    """Replace the text in an existing CRDT TitleDocument, preserving ops/metadata."""
    if not existing_td_b64:
        return encode_title(new_text)
    try:
        compressed = base64.b64decode(existing_td_b64)
        if compressed[:2] == b"\x1f\x8b":
            raw = gzip.decompress(compressed)
        elif compressed[0] == 0x78:
            raw = zlib.decompress(compressed)
        else:
            return encode_title(new_text)
        
        new_bytes = new_text.encode("utf-8")
        new_char_len = len(new_text)
        
        # Parse: outer -> field2(doc) -> field3(note)
        docs = _extract_field(raw, 2)
        if not docs:
            return encode_title(new_text)
        doc = docs[0]
        notes = _extract_field(doc, 3)
        if not notes:
            return encode_title(new_text)
        note = notes[0]
        
        # Rebuild note: new text (field 2), keep ops (field 3) + metadata (field 4), new attrrun (field 5)
        new_note = _field(2, 2, new_bytes)
        
        i = 0
        while i < len(note):
            tag_val = 0; shift = 0; start = i
            while i < len(note):
                b = note[i]; i += 1
                tag_val |= (b & 0x7f) << shift; shift += 7
                if not (b & 0x80): break
            fn = tag_val >> 3; wt = tag_val & 7
            if wt == 0:
                while i < len(note):
                    b = note[i]; i += 1
                    if not (b & 0x80): break
            elif wt == 2:
                length = 0; shift = 0
                while i < len(note):
                    b = note[i]; i += 1
                    length |= (b & 0x7f) << shift; shift += 7
                    if not (b & 0x80): break
                if fn in (3, 4):  # keep ops and metadata as-is
                    new_note += note[start:i + length]
                i += length
            else:
                break
        
        new_note += _field(5, 2, _field(1, 0, new_char_len))
        new_doc = _field(1, 0, 0) + _field(2, 0, 0) + _field(3, 2, new_note)
        new_outer = _field(1, 0, 0) + _field(2, 2, new_doc)
        return base64.b64encode(zlib.compress(new_outer)).decode()
    except Exception:
        return encode_title(new_text)


def cmd_edit(args, api):
    owner  = get_owner(api)
    rec    = find_reminder_by_id(api, owner, args.guid)
    tag    = rec.get("recordChangeTag")
    existing = rec.get("fields", {})

    # Only send fields that are actually changing
    fields = {}
    touched = []
    if args.title    is not None:
        existing_td = existing.get("TitleDocument", {}).get("value", "")
        fields["TitleDocument"] = {"value": replace_crdt_text(existing_td, args.title)}
        touched.append("titleDocument")
    if args.notes    is not None:
        existing_nd = existing.get("NotesDocument", {}).get("value", "")
        fields["NotesDocument"] = {"value": replace_crdt_text(existing_nd, args.notes)}
        touched.append("notesDocument")
    if args.priority is not None:
        fields["Priority"]      = {"value": PRIORITY_MAP.get(args.priority, 0)}
        touched.append("priority")
    if args.flagged:
        fields["Flagged"]       = {"value": 1}
        touched.append("flagged")
    if args.unflagged:
        fields["Flagged"]       = {"value": 0}
        touched.append("flagged")
    if touched:
        import time as _time
        fields["LastModifiedDate"] = {"value": int(_time.time() * 1000), "type": "TIMESTAMP"}
        fields["ResolutionTokenMap"] = {"value": _bump_resolution_map(existing, touched), "type": "STRING"}
    if args.due is not None:
        try:
            fmt = "%Y-%m-%dT%H:%M" if "T" in args.due else "%Y-%m-%d"
            d = datetime.strptime(args.due, fmt)
            fields["DueDate"] = {"value": int(d.timestamp() * 1000)}
        except ValueError as e:
            die(f"Invalid date: {e}")

    rn = rec.get("recordName")
    ck_post(api, "records/modify", {
        "zoneID": zone_id(owner),
        "atomic": True,
        "operations": [{"operationType": "update", "record": {
            "recordType": rec.get("recordType", "Reminder"),
            "recordName": rn,
            "recordChangeTag": tag,
            "fields": fields,
            "parent": rec.get("parent"),
        }}],
    })
    print(f"Updated: {rn}")

def cmd_delete(args, api):
    owner  = get_owner(api)
    rec    = find_reminder_by_id(api, owner, args.guid)
    tag    = rec.get("recordChangeTag")
    rn     = rec.get("recordName")

    # Soft delete: Deleted=1, empty title, bump ResolutionTokenMap.
    # The counter bump ensures the Mac's CRDT resolver treats this as newer
    # than its local state, preventing it from re-pushing the old record.
    existing = rec.get("fields", {})
    fields = {
        "Deleted": {"value": 1},
        "TitleDocument": {"value": encode_title("")},
        "LastModifiedDate": {"value": int(time.time() * 1000), "type": "TIMESTAMP"},
        "ResolutionTokenMap": {"value": _bump_resolution_map(existing, ["titleDocument", "deleted"]), "type": "STRING"},
    }
    ck_post(api, "records/modify", {
        "zoneID": zone_id(owner),
        "operations": [{"operationType": "update", "record": {
            "recordType": rec.get("recordType", "Reminder"),
            "recordName": rn,
            "recordChangeTag": tag,
            "fields": fields,
            "parent": rec.get("parent"),
        }}],
    })
    print(f"Deleted: {bare_id(rn)}")




def main():
    parser = argparse.ArgumentParser(prog="reminders_cli", description="iCloud Reminders CLI")
    parser.add_argument("--json", action="store_true", help="JSON output")
    sub = parser.add_subparsers(dest="command")

    sub.add_parser("auth",  help="Authenticate with iCloud")
    sub.add_parser("sync",  help="Refresh and show summary")
    sub.add_parser("lists", help="Show all reminder lists")

    p_list = sub.add_parser("list", help="List reminders")
    p_list.add_argument("--list", "-l", help="Filter by list name")

    p_add = sub.add_parser("add", help="Add a reminder")
    p_add.add_argument("title")
    p_add.add_argument("--list", "-l", dest="list")
    p_add.add_argument("--notes", "-n")
    p_add.add_argument("--priority", "-p", choices=["high", "medium", "low", "none"])
    p_add.add_argument("--due", "-d")

    p_edit = sub.add_parser("edit", help="Edit a reminder")
    p_edit.add_argument("guid")
    p_edit.add_argument("--title", "-t")
    p_edit.add_argument("--notes", "-n")
    p_edit.add_argument("--priority", "-p", choices=["high", "medium", "low", "none"])
    p_edit.add_argument("--due", "-d")
    p_edit.add_argument("--flagged",   action="store_true")
    p_edit.add_argument("--unflagged", action="store_true")

    p_del = sub.add_parser("delete", help="Complete/delete a reminder")
    p_del.add_argument("guid")

    args = parser.parse_args()
    if not args.command:
        parser.print_help()
        sys.exit(1)

    api = get_api(prompt_creds=(args.command == "auth"))
    cmds = {
        "auth": cmd_auth, "sync": cmd_sync, "lists": cmd_lists,
        "list": cmd_list, "add": cmd_add, "edit": cmd_edit, "delete": cmd_delete,
    }
    try:
        cmds[args.command](args, api)
    except KeyboardInterrupt:
        sys.exit(130)
    except Exception as e:
        die(f"Error: {e}")

if __name__ == "__main__":
    main()
