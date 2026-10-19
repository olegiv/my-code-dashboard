# Security policy

This is a local, single-user report aggregator. The current development series is supported through fixes on the default branch; there is no published stable release yet.

## Reporting

Use GitHub's **Report a vulnerability** entry when private reporting has been enabled. Until a private channel is configured, open an issue asking for a private contact without including exploit details, tokens, literal secrets, private repository names, imported findings, or corporate hosts. Do not assume private reporting is already enabled.

Include the version, platform, synthetic reproduction, impact, and affected trust boundary. Sanitize logs, screenshots, database exports, and provider responses. Do not upload a real installation database.

## Boundaries

- Only existing reports are read. GitLab GraphQL requests contain fixed read-only queries; no provider mutation API is implemented.
- Local HTTP access is protected against unexpected Host headers, cross-origin mutations, and CSRF. Provider text is escaped; script/style sources are local and constrained by CSP.
- Provider URLs require HTTPS. Requests and pagination cannot move credentials to another origin. System trust handles corporate CAs; there is no insecure TLS switch.
- Secrets are held in the OS credential backend or external private files. No automatic plaintext fallback exists. Finding response fields are allowlisted; secret values, locations, raw responses, and source snippets are not persisted.
- SQLite and backup files contain sensitive report data without application-level encryption. Use an encrypted disk, private account, and protected backups.
- Local applications running as the same OS user and browser extensions with broad access are outside the authentication boundary. There is no multi-user/server authentication.

Security review should prioritize credential leakage, finding redaction, HTML/URL injection, CSRF/DNS rebinding, unsafe provider redirects, incomplete pagination interpreted as clean results, false resolution, archive traversal, and disclosure through build artifacts or fixtures. Availability failures must retain data and remain visible. Tests must never use real credentials, corporate identifiers, production databases, or live scanner executions.

Do not publish corporate reports or expose the loopback app through a public proxy without an explicit deployment and data-permission decision.
