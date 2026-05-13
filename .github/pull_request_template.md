## Summary

-

## Verification

- [ ] `make verify`
- [ ] `go test ./... -count=1`
- [ ] `gitleaks detect --source . --no-git --redact --no-banner`

## Secret handling / redaction

- [ ] No raw tokens, keys, cookies, account material, `.env` values, or private config are included.
- [ ] Test fixtures and docs use placeholders or redacted values only.
- [ ] Gitleaks findings are resolved or intentionally allowlisted with a narrow rationale.

## Ops / rollout boundary

- [ ] No service restart, deploy, migration, or production config change is included.
- [ ] If rollout is required later, the commands and rollback path are documented for a separate operator step.

## Commit hygiene

- [ ] Changes are staged by explicit path; no `git add .`.
- [ ] Local `.hermes/` artifacts, backups, logs, and generated binaries are excluded.
- [ ] Commit slices are reviewable and do not mix unrelated concerns.
