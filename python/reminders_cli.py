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
    for attempt in range(4):
        resp = api.session.post(f"{ck_url(api)}/{path}", params=ck_params(api), json=body)
        if resp.status_code == 503:
            retry_after = 2 ** attempt  # 1, 2, 4, 8 seconds
            try:
                retry_after = max(retry_after, int(resp.json().get("retryAfter", retry_after)))
            except Exception:
                pass
            import sys
            print(f"CloudKit throttled, retrying in {retry_after}s...", file=sys.stderr)
            time.sleep(retry_after)
            continue
        resp.raise_for_status()
        return resp.json()
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

    now_ms = int(time.time() * 1000)
    now_apple = time.time() - 978307200
    replica = str(uuid.uuid4()).upper()
    touched = ["titleDocument", "completed", "list", "allDay", "flagged", "priority", "lastModifiedDate"]
    rtm = {"map": {k: {"counter": 0, "modificationTime": now_apple, "replicaID": replica} for k in touched}}

    fields = {
        "TitleDocument":    {"value": encode_title(args.title)},
        "Completed":        {"value": 0},
        "Deleted":          {"value": 0},
        "Flagged":          {"value": 0},
        "AllDay":           {"value": 0},
        "Imported":         {"value": 0},
        "Priority":         {"value": PRIORITY_MAP.get(args.priority, 0) if args.priority else 0},
        "List":             {"value": {"recordName": f"List/{list_uuid}", "action": "VALIDATE"}, "type": "REFERENCE"},
        "LastModifiedDate": {"value": now_ms, "type": "TIMESTAMP"},
        "ResolutionTokenMap": {"value": json.dumps(rtm), "type": "STRING"},
    }
    if args.notes:
        fields["NotesDocument"] = {"value": encode_title(args.notes)}
        rtm["map"]["notesDocument"] = {"counter": 0, "modificationTime": now_apple, "replicaID": replica}
        fields["ResolutionTokenMap"] = {"value": json.dumps(rtm), "type": "STRING"}
    if args.due:
        try:
            fmt = "%Y-%m-%dT%H:%M" if "T" in args.due else "%Y-%m-%d"
            d = datetime.strptime(args.due, fmt)
            fields["DueDate"] = {"value": int(d.timestamp() * 1000)}
        except ValueError as e:
            die(f"Invalid date: {e}")

    rec_name = str(uuid.uuid4()).upper()
    list_ref = f"List/{list_uuid}"
    resp = ck_post(api, "records/modify", {
        "zoneID": zone_id(owner),
        "atomic": True,
        "operations": [{"operationType": "create", "record": {
            "recordType": "Reminder",
            "recordName": rec_name,
            "fields": fields,
            "parent": {"recordName": list_ref},
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
        entry["counter"] = max(entry.get("counter", 0) + 1, 100)
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

    # Soft delete: Deleted=1, Completed=1, empty title, high CRDT counters.
    # Uses the existing replicaID with counter=999 so the Mac's CRDT resolver
    # treats this as authoritative and won't re-push from local state.
    existing = rec.get("fields", {})
    now_apple = time.time() - 978307200
    rtm = _bump_resolution_map(existing,
        ["titleDocument", "deleted", "completed", "lastModifiedDate"])
    fields = {
        "Deleted": {"value": 1},
        "Completed": {"value": 1},
        "TitleDocument": {"value": encode_title("")},
        "LastModifiedDate": {"value": int(time.time() * 1000), "type": "TIMESTAMP"},
        "ResolutionTokenMap": {"value": rtm, "type": "STRING"},
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




# ---------------------------------------------------------------------------
# Queue adapters
# ---------------------------------------------------------------------------

import sys as _sys
_sys.path.insert(0, _sys.path[0])  # ensure local imports work

from state import StateDB as _StateDB
from sebastian_queue import (
    QueueItem, QueueManager, ChecklistItem,
    PRIORITY_MAP as _PMAP, _parse_checklist_line, render_notes,
    _deserialize_item, _serialize_item,
)

_DB_PATH = Path.home() / ".config" / "icloud-reminders" / "state.db"


class _StateDBAdapter:
    """Bridge QueueManager's set_queue_item/get_queue_item to StateDB's upsert API."""
    def __init__(self, db: _StateDB):
        self._db = db

    def get_queue_item(self, key: str):
        row = self._db.get_queue_item(key)
        if not row:
            return None
        children = {}
        for c in self._db.list_queue_children(key):
            ck = c["child_key"]
            children[ck] = {"key": ck, "title": c["title"], "cloud_id": c.get("cloud_id",""),
                "due": c.get("due_at"), "priority": c.get("priority_value",0),
                "flagged": bool(c.get("flagged",0)), "updated_at": c.get("updated_at","")}
        import json as _json
        row["checklist"] = _json.loads(row.get("checklist_json") or "[]")
        row["tags"] = _json.loads(row.get("tags_json") or "[]")
        row["blocked"] = bool(row.get("blocked", 0))
        row["flagged"] = bool(row.get("flagged", 0) if "flagged" in row else False)
        row["priority"] = row.get("priority_value", 0)
        row["status_line"] = row.get("status_line") or ""
        row["executor"] = row.get("executor") or ""
        row["due"] = row.get("due_at")
        row["section"] = row.get("section_name") or ""
        row["notes"] = row.get("legacy_notes") or ""
        row["hours_budget"] = row.get("hours_budget", 0.0)
        row["tokens_budget"] = row.get("tokens_budget", 0)
        row["cloud_id"] = row.get("cloud_id") or ""
        row["updated_at"] = row.get("updated_at") or ""
        row["children"] = children
        row["key"] = key
        row["title"] = row.get("title", "")
        return row

    def set_queue_item(self, key: str, data: dict):
        import json as _json
        checklist = data.get("checklist", [])
        tags = data.get("tags", [])
        children = data.pop("children", {})
        self._db.upsert_queue_item(
            key, data["title"],
            cloud_id=data.get("cloud_id") or None,
            section_name=data.get("section") or None,
            tags_json=_json.dumps(tags),
            priority_value=data.get("priority", 0),
            due_at=data.get("due"),
            status_line=data.get("status_line") or None,
            checklist_json=_json.dumps(checklist),
            hours_budget=data.get("hours_budget", 0.0),
            tokens_budget=data.get("tokens_budget", 0),
            executor=data.get("executor") or None,
            blocked=int(bool(data.get("blocked", False))),
            legacy_notes=data.get("notes") or None,
            updated_at=data.get("updated_at"),
        )
        for ck, cv in children.items():
            self._db.upsert_queue_child(
                key, ck, cv["title"],
                cloud_id=cv.get("cloud_id") or None,
                due_at=cv.get("due"),
                priority_value=cv.get("priority", 0),
                flagged=int(bool(cv.get("flagged", False))),
                updated_at=cv.get("updated_at") or None,
            )

    def delete_queue_item(self, key: str):
        self._db.delete_queue_item(key)

    def list_queue_items(self) -> list:
        items = []
        for row in self._db.list_queue_items():
            enriched = self.get_queue_item(row["queue_key"])
            if enriched:
                items.append(enriched)
        return items


class _RemindersAPI:
    """Duck-typed adapter wiring QueueManager to the CloudKit helpers."""
    def __init__(self, api, owner, list_name="Sebastian"):
        self._api = api
        self._owner = owner
        self._list_name = list_name

    def _list_uuid(self):
        return find_list(self._api, self._owner, self._list_name)["uuid"]

    def create_reminder(self, title, list_name=None, **kwargs):
        lname = list_name or self._list_name
        lst = find_list(self._api, self._owner, lname)
        list_uuid = lst["uuid"]
        now_ms = int(time.time() * 1000)
        now_apple = time.time() - 978307200
        replica = str(uuid.uuid4()).upper()
        touched = ["titleDocument", "completed", "list", "allDay", "flagged", "priority", "lastModifiedDate"]
        rtm = {"map": {k: {"counter": 0, "modificationTime": now_apple, "replicaID": replica} for k in touched}}
        prio = kwargs.get("priority", 0)
        if isinstance(prio, str):
            prio = PRIORITY_MAP.get(prio, 0)
        fields = {
            "TitleDocument":    {"value": encode_title(title)},
            "Completed":        {"value": 0},
            "Deleted":          {"value": 0},
            "Flagged":          {"value": int(bool(kwargs.get("flagged", False)))},
            "AllDay":           {"value": 0},
            "Imported":         {"value": 0},
            "Priority":         {"value": prio},
            "List":             {"value": {"recordName": f"List/{list_uuid}", "action": "VALIDATE"}, "type": "REFERENCE"},
            "LastModifiedDate": {"value": now_ms, "type": "TIMESTAMP"},
            "ResolutionTokenMap": {"value": json.dumps(rtm), "type": "STRING"},
        }
        notes = kwargs.get("notes")
        if notes:
            rtm["map"]["notesDocument"] = {"counter": 0, "modificationTime": now_apple, "replicaID": replica}
            fields["NotesDocument"] = {"value": encode_title(notes)}
            fields["ResolutionTokenMap"] = {"value": json.dumps(rtm), "type": "STRING"}
        due = kwargs.get("due")
        if due:
            try:
                fmt = "%Y-%m-%dT%H:%M" if "T" in due else "%Y-%m-%d"
                fields["DueDate"] = {"value": int(datetime.strptime(due, fmt).timestamp() * 1000)}
            except ValueError:
                pass
        rec_name = str(uuid.uuid4()).upper()
        resp = ck_post(self._api, "records/modify", {
            "zoneID": zone_id(self._owner),
            "atomic": True,
            "operations": [{"operationType": "create", "record": {
                "recordType": "Reminder", "recordName": rec_name,
                "fields": fields, "parent": {"recordName": f"List/{list_uuid}"},
            }}],
        })
        saved = bare_id(resp.get("records", [{}])[0].get("recordName", rec_name))
        return {"id": saved}

    def edit_reminder(self, guid, **kwargs):
        rec = find_reminder_by_id(self._api, self._owner, guid)
        tag = rec.get("recordChangeTag")
        existing = rec.get("fields", {})
        fields = {}
        touched = []
        title = kwargs.get("title")
        if title is not None:
            fields["TitleDocument"] = {"value": replace_crdt_text(existing.get("TitleDocument",{}).get("value",""), title)}
            touched.append("titleDocument")
        notes = kwargs.get("notes")
        if notes is not None:
            fields["NotesDocument"] = {"value": replace_crdt_text(existing.get("NotesDocument",{}).get("value",""), notes)}
            touched.append("notesDocument")
        prio = kwargs.get("priority")
        if prio is not None:
            if isinstance(prio, str): prio = PRIORITY_MAP.get(prio, 0)
            fields["Priority"] = {"value": prio}
            touched.append("priority")
        flagged = kwargs.get("flagged")
        if flagged is not None:
            fields["Flagged"] = {"value": int(bool(flagged))}
            touched.append("flagged")
        if touched:
            fields["LastModifiedDate"] = {"value": int(time.time() * 1000), "type": "TIMESTAMP"}
            fields["ResolutionTokenMap"] = {"value": _bump_resolution_map(existing, touched), "type": "STRING"}
        due = kwargs.get("due")
        if due is not None:
            try:
                fmt = "%Y-%m-%dT%H:%M" if "T" in due else "%Y-%m-%d"
                fields["DueDate"] = {"value": int(datetime.strptime(due, fmt).timestamp() * 1000)}
            except ValueError:
                pass
        if not fields:
            return {}
        ck_post(self._api, "records/modify", {
            "zoneID": zone_id(self._owner),
            "atomic": True,
            "operations": [{"operationType": "update", "record": {
                "recordType": rec.get("recordType", "Reminder"),
                "recordName": rec.get("recordName"), "recordChangeTag": tag,
                "fields": fields, "parent": rec.get("parent"),
            }}],
        })
        return {}

    def _soft_delete(self, guid):
        rec = find_reminder_by_id(self._api, self._owner, guid)
        tag = rec.get("recordChangeTag")
        rn = rec.get("recordName")
        existing = rec.get("fields", {})
        rtm = _bump_resolution_map(existing, ["titleDocument", "deleted", "completed", "lastModifiedDate"])
        ck_post(self._api, "records/modify", {
            "zoneID": zone_id(self._owner),
            "operations": [{"operationType": "update", "record": {
                "recordType": rec.get("recordType", "Reminder"),
                "recordName": rn, "recordChangeTag": tag,
                "fields": {
                    "Deleted": {"value": 1}, "Completed": {"value": 1},
                    "TitleDocument": {"value": encode_title("")},
                    "LastModifiedDate": {"value": int(time.time() * 1000), "type": "TIMESTAMP"},
                    "ResolutionTokenMap": {"value": rtm, "type": "STRING"},
                },
                "parent": rec.get("parent"),
            }}],
        })

    def complete_reminder(self, guid):
        self._soft_delete(guid)
        return {}

    def delete_reminder(self, guid):
        self._soft_delete(guid)
        return {}


class _NoopRemindersAPI:
    """Stub that skips CloudKit — queue commands write SQLite only."""
    def __init__(self, db_adapter):
        self._db = db_adapter
    def create_reminder(self, title, list_name=None, **kw):
        return {"guid": "pending-sync", "cloud_id": None}
    def edit_reminder(self, guid, **kw):
        pass
    def complete_reminder(self, guid):
        pass
    def delete_reminder(self, guid):
        pass
    def get_reminders(self, list_name=None):
        return []


def _make_mgr_local(args):
    """Build a QueueManager backed by SQLite only — no CloudKit, no auth."""
    db = _StateDBAdapter(_StateDB(str(_DB_PATH)))
    rem_api = _NoopRemindersAPI(db)
    list_name = getattr(args, "list", None) or "Sebastian"
    return QueueManager(db, rem_api, list_name), db


def _make_mgr(api, args):
    owner = get_owner(api)
    list_name = getattr(args, "list", None) or "Sebastian"
    db = _StateDBAdapter(_StateDB(str(_DB_PATH)))
    rem_api = _RemindersAPI(api, owner, list_name)
    return QueueManager(db, rem_api, list_name), db


# ---------------------------------------------------------------------------
# Queue command handlers
# ---------------------------------------------------------------------------

def cmd_queue_upsert(args, api):
    mgr, _ = _make_mgr_local(args)
    items_raw = getattr(args, "item", []) or []
    checklist = [_parse_checklist_line(r) for r in items_raw]
    prio = _PMAP.get(getattr(args, "priority", "") or "", 0)
    flagged = True if getattr(args, "flagged", False) else (False if getattr(args, "unflagged", False) else None)
    blocked = True if getattr(args, "blocked", False) else (False if getattr(args, "unblocked", False) else None)
    kw = dict(
        section=getattr(args, "section", None),
        tags=getattr(args, "tag", None) or None,
        priority=prio if getattr(args, "priority", None) else None,
        due=getattr(args, "due", None),
        flagged=flagged,
        status_line=getattr(args, "status", None),
        checklist=checklist if checklist else None,
        hours_budget=getattr(args, "hours_budget", None),
        tokens_budget=getattr(args, "tokens_budget", None),
        executor=getattr(args, "executor", None),
        blocked=blocked,
    )
    kw = {k: v for k, v in kw.items() if v is not None}
    title = getattr(args, "title", None)
    if not title:
        # load existing for update-only
        existing = mgr._db.get_queue_item(args.key)
        if not existing:
            die("--title required when creating a new queue item")
        title = existing["title"]
    item = mgr.upsert(args.key, title, **kw)
    print(f"upserted: {item.key}  cloud_id={item.cloud_id}  title={item.title!r}")


def cmd_queue_state_json(args, api):
    db = _StateDBAdapter(_StateDB(str(_DB_PATH)))
    items = db.list_queue_items()
    out = []
    for raw in items:
        item = _deserialize_item(raw)
        d = _serialize_item(item)
        out.append(d)
    print(json.dumps(out, indent=2, default=str))


def cmd_queue_complete(args, api):
    mgr, _ = _make_mgr_local(args)
    mgr.close(args.key, complete=True)
    print(f"completed: {args.key}")


def cmd_queue_delete(args, api):
    mgr, _ = _make_mgr_local(args)
    mgr.close(args.key, complete=False)
    print(f"deleted: {args.key}")


def cmd_queue_child_upsert(args, api):
    mgr, _ = _make_mgr_local(args)
    prio = _PMAP.get(getattr(args, "priority", "") or "", 0)
    flagged = True if getattr(args, "flagged", False) else (False if getattr(args, "unflagged", False) else None)
    kw = dict(
        due=getattr(args, "due", None),
        priority=prio if getattr(args, "priority", None) else None,
        flagged=flagged,
    )
    kw = {k: v for k, v in kw.items() if v is not None}
    child = mgr.upsert_child(args.parent_key, args.child_key, args.title, **kw)
    print(f"child upserted: {args.parent_key}/{child.key}  cloud_id={child.cloud_id}")


def cmd_queue_child_complete(args, api):
    mgr, _ = _make_mgr_local(args)
    mgr.close_child(args.parent_key, args.child_key)
    print(f"child completed: {args.parent_key}/{args.child_key}")


def cmd_queue_refresh(args, api):
    owner = get_owner(api)
    db_adapter = _StateDBAdapter(_StateDB(str(_DB_PATH)))
    raw = db_adapter.get_queue_item(args.key)
    if not raw:
        die(f"Queue key {args.key!r} not found")
    item = _deserialize_item(raw)
    notes = render_notes(item)
    rem_api = _RemindersAPI(api, owner, "Sebastian")
    if not item.cloud_id:
        die(f"No cloud_id for {args.key!r}; run queue-upsert first")
    rem_api.edit_reminder(item.cloud_id, notes=notes)
    print(f"refreshed: {args.key}  cloud_id={item.cloud_id}")


def cmd_queue_sync(args, api):
    """Push all queue items to Apple Reminders."""
    owner = get_owner(api)
    list_name = getattr(args, "list", None) or "Sebastian"
    db = _StateDBAdapter(_StateDB(str(_DB_PATH)))
    rem_api = _RemindersAPI(api, owner, list_name)
    items = db.list_queue_items()
    synced = 0
    for raw in items:
        item = _deserialize_item(raw)
        notes = render_notes(item)
        title = item.title
        if item.blocked:
            title = title.rstrip() + " [blocked]" if "[blocked]" not in title else title
        try:
            if not item.cloud_id:
                result = rem_api.create_reminder(title, priority=item.priority,
                    notes=notes, flagged=item.flagged, due=item.due)
                cloud_id = result.get("cloud_id") or result.get("guid")
                if cloud_id:
                    db._db.upsert_queue_item(item.key, item.title, cloud_id=cloud_id)
                    print(f"  created: {item.key} -> {cloud_id}")
            else:
                rem_api.edit_reminder(item.cloud_id, title=title, notes=notes,
                    priority=item.priority, flagged=item.flagged)
                print(f"  synced: {item.key} ({item.cloud_id})")
            synced += 1
        except Exception as e:
            print(f"  FAILED: {item.key}: {e}", file=sys.stderr)
    print(f"Synced {synced}/{len(items)} items")


def cmd_queue_audit(args, api):
    owner = get_owner(api)
    db_adapter = _StateDBAdapter(_StateDB(str(_DB_PATH)))
    db_items = {row["key"]: _deserialize_item(row) for row in db_adapter.list_queue_items()}

    lst = find_list(api, owner, "Sebastian")
    cloud_rems = get_reminders(api, owner, lst["uuid"])
    cloud_by_id = {}
    for r in cloud_rems:
        cid = bare_id(r.get("recordName",""))
        title = extract_title(r.get("fields",{}).get("TitleDocument",{}).get("value",""))
        cloud_by_id[cid] = title

    db_cloud_ids = {item.cloud_id: key for key, item in db_items.items() if item.cloud_id}
    mismatches = []
    for key, item in db_items.items():
        if item.cloud_id and item.cloud_id not in cloud_by_id:
            mismatches.append(f"MISSING_IN_CLOUD  key={key}  cloud_id={item.cloud_id}")
        elif not item.cloud_id:
            mismatches.append(f"NO_CLOUD_ID       key={key}  title={item.title!r}")
    for cid, ctitle in cloud_by_id.items():
        if cid not in db_cloud_ids:
            mismatches.append(f"UNTRACKED_IN_DB   cloud_id={cid}  title={ctitle!r}")
    if not mismatches:
        print("OK -- queue state matches Apple Reminders")
    else:
        for m in mismatches:
            print(m)


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

    # Queue subcommands
    p_qu = sub.add_parser("queue-upsert", help="Create or update a queue item")
    p_qu.add_argument("key")
    p_qu.add_argument("--title")
    p_qu.add_argument("--list", default="Sebastian")
    p_qu.add_argument("--section")
    p_qu.add_argument("--tag", dest="tag", action="append", default=[])
    p_qu.add_argument("--priority", choices=["high","medium","low","none"])
    p_qu.add_argument("--executor")
    p_qu.add_argument("--hours-budget", dest="hours_budget", type=float)
    p_qu.add_argument("--tokens-budget", dest="tokens_budget", type=int)
    p_qu.add_argument("--status")
    p_qu.add_argument("--item", action="append", default=[])
    p_qu.add_argument("--blocked",   dest="blocked",   action="store_true", default=False)
    p_qu.add_argument("--unblocked", dest="unblocked", action="store_true", default=False)
    p_qu.add_argument("--due")
    p_qu.add_argument("--flagged",   dest="flagged",   action="store_true", default=False)
    p_qu.add_argument("--unflagged", dest="unflagged", action="store_true", default=False)

    sub.add_parser("queue-state-json", help="Dump all queue items as JSON")

    p_qc = sub.add_parser("queue-complete", help="Mark queue item complete")
    p_qc.add_argument("key")

    p_qd = sub.add_parser("queue-delete", help="Delete (abandon) a queue item")
    p_qd.add_argument("key")

    p_qcu = sub.add_parser("queue-child-upsert", help="Create or update a child task")
    p_qcu.add_argument("parent_key")
    p_qcu.add_argument("child_key")
    p_qcu.add_argument("--title", required=True)
    p_qcu.add_argument("--due")
    p_qcu.add_argument("--priority", choices=["high","medium","low","none"])
    p_qcu.add_argument("--flagged",   action="store_true", default=False)
    p_qcu.add_argument("--unflagged", action="store_true", default=False)

    p_qcc = sub.add_parser("queue-child-complete", help="Complete a child task")
    p_qcc.add_argument("parent_key")
    p_qcc.add_argument("child_key")

    p_qr = sub.add_parser("queue-refresh", help="Re-render and push notes for a queue item")
    p_qr.add_argument("key")

    p_qsync = sub.add_parser("queue-sync", help="Push queue items to Apple Reminders")
    p_qsync.add_argument("--list", "-l", default="Sebastian")
    sub.add_parser("queue-audit", help="Compare queue DB with Apple Reminders")

    args = parser.parse_args()
    if not args.command:
        parser.print_help()
        sys.exit(1)

    # Local-only commands skip iCloud auth entirely (fast, no network)
    LOCAL_CMDS = {
        "queue-state-json": cmd_queue_state_json,
        "queue-upsert": cmd_queue_upsert,
        "queue-complete": cmd_queue_complete,
        "queue-delete": cmd_queue_delete,
        "queue-child-upsert": cmd_queue_child_upsert,
        "queue-child-complete": cmd_queue_child_complete,
    }
    if args.command in LOCAL_CMDS:
        try:
            LOCAL_CMDS[args.command](args, None)
        except Exception as e:
            die(f"Error: {e}")
        return

    api = get_api(prompt_creds=(args.command == "auth"))
    cmds = {
        "auth": cmd_auth, "sync": cmd_sync, "lists": cmd_lists,
        "list": cmd_list, "add": cmd_add, "edit": cmd_edit, "delete": cmd_delete,
        "queue-upsert": cmd_queue_upsert,
        "queue-state-json": cmd_queue_state_json,
        "queue-complete": cmd_queue_complete,
        "queue-delete": cmd_queue_delete,
        "queue-child-upsert": cmd_queue_child_upsert,
        "queue-child-complete": cmd_queue_child_complete,
        "queue-refresh": cmd_queue_refresh,
        "queue-sync": cmd_queue_sync,
        "queue-audit": cmd_queue_audit,
    }
    try:
        cmds[args.command](args, api)
    except KeyboardInterrupt:
        sys.exit(130)
    except Exception as e:
        die(f"Error: {e}")

if __name__ == "__main__":
    main()
