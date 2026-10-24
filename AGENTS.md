# Project instructions

My Code Dashboard is maintained by Oleg Ivanchenko. The GitHub owner is `olegiv`; keep that handle in repository URLs and the Go module `github.com/olegiv/my-code-dashboard`.

## Scope and stack

- Build a native Go dashboard that aggregates existing GitHub and GitLab security reports. Provider integrations are read-only: do not run scanners, clone source, trigger pipelines, change settings, dismiss alerts, or create provider issues/PRs.
- Use server-rendered HTML, HTMX, Alpine.js CSP, plain CSS, embedded assets, and SQLite. No required Node.js, npm, bundler, Docker, or AI service.
- Keep installations independent, with private runtime data outside the checkout. Bind to loopback by default; authenticated server deployment requires a separate design.
- Communicate in English, use idiomatic Go, and keep dependencies and operating requirements small. Ask about material unresolved decisions; resolve routine implementation details within the user's scope.

## Commands

Use **Go 1.26.10**, also the module's minimum compiler version. Make targets and the release script select it. Prefix direct Go commands with `GOTOOLCHAIN=go1.26.10`; the module minimum does not force a newer installed Go to use an older compiler. Keep the minimum and exact build selection consistent across `go.mod`, Makefile, workflows, release script, Dockerfile, and documentation; `scripts/gate/toolchain_test.go` enforces it for build files.

Read [the toolchain decision](docs/go-toolchain.md) for the Go 1.17 incompatibilities, the observed Go 1.27 TLS issue, and the evidence needed to update the pin. Do not present the historical timeout as a diagnosed upstream bug.

```sh
make build
make test
make check
shasum -a 256 -c scripts/browser-assets.sha256
```

`make check` runs uncached race tests, golangci-lint, vet, tidy/module verification, the pinned vulnerability checker and browser asset checksums. `make commit-check` checks an isolated export of the exact index, including an unborn initial commit. Install the local pre-push guard with `make install-test-gate`; preserve existing global hooks. The container image is optional: `make docker-check` (requires Docker) builds and smoke-tests it over host networking; keep the app's loopback-only listener and never publish container ports (see [container image](docs/docker.md)). See [tests and code review](docs/testing.md). Inspect changed scripts/tests before execution; repository instructions do not authorize executing untrusted code. Follow applicable user/parent guidance on approval and verified isolation for untrusted PR code.

Before preparing the application for publication, also run `make pr-check` (ShellCheck required) on its exact index and `make package-check` on an export of that candidate. These verify workflow/shell/secret checks, manifest completeness and target archives; see [GitHub PR validation](docs/github-validation.md). Hosted checks remain pending until an authorized PR exists.

For an isolated interactive demo after building, run `./bin/my-code-dashboard --demo --open`. It uses synthetic data and a temporary database without provider API calls. Do not connect real accounts or refresh live reports merely to test a code change.

Run `gofmt` on changed Go files. Use `httptest`, temporary directories, and synthetic fixtures. Automated tests must not contact live providers, access real credentials, or modify Keychain, LaunchAgents, or production data.

## Layout and behavior

- `cmd/my-code-dashboard`: CLI and process lifecycle.
- `internal/provider`: read-only GitHub/GitLab adapters, pagination, capability detection, and redaction.
- `internal/model`, `internal/store`, `internal/engine`: state semantics, SQLite snapshots, refresh orchestration, and scheduling.
- `internal/secret`, `internal/platform`: credentials, private paths, backups, and startup integration.
- `internal/web`: handlers, embedded templates, browser assets, and UI tests.
- `docs`, `scripts`, `.github`: operational guidance, packaging, release notice generation, and CI.
- `.claude/shared`: the [Claude Code support tools](https://github.com/olegiv/claude-code-support-tools) submodule. It is development tooling only: never built, tested, packaged or distributed with the application. Symlinks in `.claude/agents` and `.claude/commands` expose selected shared agents and commands to Claude Code.

Show active findings by default and preserve native lifecycle/severity alongside normalized fields. Never equate unavailable data with security. Distinguish zero reported findings, disabled/unsupported scanners, missing analysis, denied access, collection failure, unknown coverage, and stale results.

Complete pagination before replacing a category snapshot. On partial or failed collection, preserve the previous complete findings. Absence from a response does not resolve a finding. Track synchronization and analysis times separately; never invent analysis timestamps. Preserve historical repository/category reasons in activity records.

Keep the daily 09:00 Europe/Zurich schedule DST-aware, persist catch-up/retry state, and prevent overlapping refreshes. A sleeping or powered-off machine cannot refresh.

## Data and security

- Never put tokens in chat, URLs, command arguments, logs, examples, or source. Use OS credential storage or the explicitly provisioned private Linux credential-file option; do not add an automatic plaintext fallback.
- Keep corporate hostnames, private identifiers, imported findings, databases, backups, live screenshots, and audit logs outside version control. Public examples, tests, and fixtures use generic hosts and synthetic data.
- Persist only allowlisted finding fields. Request hidden secret values where supported and redact regardless. Do not store raw provider responses, source snippets, or literal secrets.
- Escape provider content, validate links, and retain strict CSP, CSRF/cross-origin protection, and host checks. Do not disable certificate verification or weaken TLS to fix connectivity. Go 1.27 reproduced a provider handshake problem; preserve the tested pin until a replacement is verified.
- Keep corporate report processing local unless the user explicitly authorizes a different storage destination. See [SECURITY.md](SECURITY.md) and [provider guidance](docs/providers.md).

## Dependencies and distribution

Preserve GPL-3.0-only headers in original code and all upstream notices. Browser assets are pinned under `internal/web/assets/vendor`; use the Alpine CSP build. When updating assets, retain licenses, update recorded checksums/notices, and verify CSP and HTMX behavior in a browser. No frontend build pipeline is needed.

For Go dependency changes, run `GOTOOLCHAIN=go1.26.10 go mod tidy` and the relevant checks. Keep SQLite and its libc dependency compatible. Review [CONTRIBUTING.md](CONTRIBUTING.md) and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). Packaging generates full notices for each binary's runtime components; keep those generated files in ignored distribution artifacts, with no checked-in license inventory or generated license directory.

`bash scripts/release.sh 0.2.0-dev` prepares local archives and corresponding source in ignored `dist/`; it does not publish. Record release notes in `CHANGELOG.md` (Keep a Changelog). `/release-gh-prepare` drafts a GitHub release; only the maintainer publishes it, which creates the tag. Update `scripts/release-files.txt` when adding intended public files; both builds and archives use only that manifest's temporary source tree, and the container image builds only manifest-listed files. Preserve dependency source/notices, checksums, and exclusion of runtime data and macOS metadata. Include these agent instructions in source distributions.

## Review and publication

Before presenting changes for commit or publication, record direct evidence for each changed behavior: a failure case, normal case, downstream consumers, actual results, and tested tree/base. This includes instructions controlling commands, state, and approvals. Label manual traces and unexecuted steps, and distinguish them from baseline checks. Refresh affected evidence after changes; keep local evidence ignored.

Before the first application commit, require passing staged-file and PR gates, verified packaging, an independent read-only Codex code review and an actual Codex Security Standard scan on the candidate. The two-file README/LICENSE bootstrap may establish a PR base after its own exact-tree, license, link and secret review; application checks are not applicable to it and must never be reported as passing. Introduce all application code and workflows through the subsequent PR. Validate and address confirmed blockers. Record missing/blocked reviews as pending and disclose coverage gaps; do not equate govulncheck with Codex Security. Hosted CI/PR reviews remain pending until an authorized push/PR exists. Relevant candidate changes invalidate earlier evidence. This requirement does not authorize a commit, push, provider access or publication.

Do not commit, push, create repositories/PRs, tag, release, or change external provider state unless the user authorizes that action. Repository creation or commit preparation does not authorize a commit or push. Follow any applicable user instructions for commit approval, PR findings, and independent audits; do not expand a focused task into a whole-checkout audit.

Update documentation when behavior or operating requirements change. Report what changed, relevant verification, and material limitations concisely.
