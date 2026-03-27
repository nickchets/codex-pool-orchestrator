# Security And Redaction

Never publish:

- real auth files
- refresh tokens, access tokens, id tokens, JWT secrets, admin tokens
- live account emails, workspace IDs, account IDs, or seat keys
- workstation-specific absolute paths
- screenshots from a live operator deployment
- runtime backups or quarantine contents

Safe to publish:

- a sanitized standalone fork of the Go proxy
- generic deployment helper code
- synthetic tests
- service templates with `%h` or env-driven paths
- docs that describe layout and commands without live secrets

Before pushing:

1. Search for absolute local paths.
2. Search for real emails and workspace IDs.
3. Search for secrets and token-shaped strings.
4. Run the deployment/UI tests.
5. Run the targeted Go tests for the operator delta.
