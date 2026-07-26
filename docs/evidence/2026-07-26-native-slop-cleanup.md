# Native slop cleanup — 2026-07-26

Sanitized operational record; raw titles and EventKit IDs remain under the
git-ignored local `exports/rem-cleanup/` evidence directory.

## Result

- Backend: `eventkit/in-process`
- Deleted: 122 generated records
  - generated agent-run list: 99
  - completed MCP verification artifacts: 23
- Preserved authoritative total: 128
  - To-Do: 87
  - Follow-up: 38
  - Shopping: 2
  - Delegate: 0
  - generated agent-run list: 0
  - MCP canary list: 1 protected reminder
- Raw manifest SHA-256:
  `3778378806a196ba843389d4a79e4e6151a4bb322cea54b547ad3a68382ad93b`

## Verification

- Native delete returned `deleted: 122`.
- Post-delete EventKit queries returned zero records in the generated agent-run
  list and exactly one protected reminder in the MCP canary list.
- Normal personal-list totals were unchanged.
- `remindd` was restarted and EventKit totals were rechecked.
- Native delete was added only to the local CLI; the remote MCP tool surface was
  not expanded.
