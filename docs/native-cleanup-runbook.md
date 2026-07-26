# Native reminder cleanup runbook

Use this procedure for duplicate storms, abandoned agent-run records, test
canaries, or other reminder pollution. All inspection and deletion must go
through native macOS EventKit. Do not restore the retired private-web backend.

## Safety contract

- `reminders delete` is permanent and local CLI-only.
- Use exact EventKit IDs. Do not delete by a broad title substring.
- Inventory every list and distinguish generated clusters from normal personal
  reminders before mutating anything.
- Save a manifest and before/after counts outside git if it contains personal
  reminder content. The repository ignores `exports/` for this reason.
- Preserve a sanitized, non-personal summary in `docs/evidence/` when the
  cleanup is operationally significant.

## 1. Establish current totals

```bash
reminders lists
reminders show --list "To-Do" --completed --limit 1
reminders show --list "Follow-up" --completed --limit 1
```

Read `total_count` from each explicit list. The public response limit does not
change the total.

## 2. Build and review the deletion manifest

Fetch pages with `show --completed --limit 200 --offset <n>`, combine the JSON,
and select only the exact records belonging to the confirmed pollution cluster.
The manifest should include at least `id`, `title`, `list_name`, and `completed`.

Before deletion, record:

- total IDs;
- counts grouped by list;
- a SHA-256 hash of the manifest;
- the intended records that must remain.

Stop if the count or list distribution differs from the reviewed expectation.

## 3. Delete exact IDs

For a small set:

```bash
reminders delete <exact-id-1> <exact-id-2>
```

For a reviewed manifest, feed bounded batches rather than title searches:

```bash
xargs -n 100 reminders delete < exports/rem-cleanup/<run>/ids.txt
```

If a batch fails, stop and re-inventory. Do not blindly rerun the whole set.

## 4. Verify the authoritative store

Repeat per-list totals and explicitly check the protected records. A successful
cleanup requires:

- target lists at their expected post-cleanup counts;
- protected reminders still present;
- normal personal lists unchanged;
- `reminders status` reporting `eventkit/in-process` with Full Access.

For a large cleanup, restart the user Reminders sync daemon and reopen the app:

```bash
killall remindd
open -gj -a Reminders
```

Then repeat the EventKit counts. `remindd` is a macOS component and will be
relaunched automatically.

## 5. Resolve a stale iPhone count

If the Mac reports the correct EventKit totals but the iPhone still shows
thousands of reminders, the phone is displaying stale local state or processing
deletion tombstones. Do not create more deletes against the already-clean store.

1. Force-quit Reminders on the iPhone.
2. Restart the iPhone and allow iCloud time to synchronize.
3. If the count remains wrong, open **Settings → Apple Account → iCloud → See
   All → Reminders**.
4. Turn Reminders off and choose **Delete from iPhone**.
5. Restart the iPhone, enable Reminders again, and let it download from iCloud.

This resets the device cache; it does not delete the authoritative iCloud data.

## 6. Verify the tool after cleanup

```bash
go test ./...
./scripts/install-mcp-runtime.sh --check
```

The remote MCP surface must remain unchanged: `lists`, `show`, `get`, `add`,
`complete`, and `status`. `delete` must not appear remotely.
