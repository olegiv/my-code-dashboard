# My Code Dashboard

Self-hosted security dashboard for GitHub and GitLab repositories, built with Go, HTMX, and Alpine.js.

[![Go checks](https://github.com/olegiv/my-code-dashboard/actions/workflows/ci.yml/badge.svg)](https://github.com/olegiv/my-code-dashboard/actions/workflows/ci.yml)
[![CodeQL](https://github.com/olegiv/my-code-dashboard/actions/workflows/codeql.yml/badge.svg)](https://github.com/olegiv/my-code-dashboard/actions/workflows/codeql.yml)
[![Latest release](https://img.shields.io/github/v/release/olegiv/my-code-dashboard)](https://github.com/olegiv/my-code-dashboard/releases)
[![Go version](https://img.shields.io/github/go-mod/go-version/olegiv/my-code-dashboard?logo=go)](go.mod)
[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)

One local dashboard for existing security reports, coverage, refresh health, and changes. Runs as a native binary with embedded browser assets and SQLite. No Node.js, npm, CDN, AI service, Docker, or C compiler is required.

**Run v1 only on a trusted single-user machine.** There is no application login: other local accounts and processes can access the loopback dashboard and download backups. Loopback binding and CSRF checks do not authenticate local clients. Authentication is deferred; shared-host and internet-facing deployment are unsupported. See [the security policy](SECURITY.md).

## Run it

Go **1.26.10** is the minimum supported compiler and the pinned, security-patched build toolchain. Make targets and the release script select this version even when a newer Go is installed; Go downloads it if needed. The binary needs no Go installation at runtime.

```sh
make build
./bin/my-code-dashboard --demo --open
```

The default demo uses synthetic data and a temporary database, accepts no credentials, and makes no provider API calls. An explicit nondefault `--data-dir` retains its synthetic state. Exit with Ctrl+C. To use real accounts:

```sh
./bin/my-code-dashboard --open
```

Open [http://127.0.0.1:7364](http://127.0.0.1:7364). In **Connections**, validate a read-only token, discover repositories, then save your monitoring selection in **Repositories**. Use **Refresh now** to import their existing reports. Tokens belong in the local GUI or a provisioned credential file, never in chat, command arguments, URLs, or source files.

The app imports reports. It does not run scanners, clone repositories, enable features, dismiss alerts, trigger pipelines, or modify providers. Daily synchronization is independent of the providers' scanning schedules.

## What is included

- GitHub public/private repository discovery; Dependabot, code scanning, and secret scanning alerts.
- Self-managed GitLab discovery and schema-aware GraphQL vulnerability reports, including native categories and lifecycle states.
- Portfolio and repository pages; severity, category, provider, repository, group/owner, native status, and text filters.
- Separate collection time, available analysis evidence, coverage gaps, stale evidence, and collection failures.
- Refresh activity with separate disabled/missing-analysis counts and repository/category reasons retained for each run.
- Changes against the preceding successful category import. First imports establish a baseline.
- Daily 09:00 Europe/Zurich refresh, manual refresh, persisted missed-run catch-up, bounded retries, and overlap prevention.
- macOS Keychain; Linux Secret Service or externally provisioned private credential files.
- GUI backup export, validated offline restore, per-user macOS LaunchAgent installation, and a Linux systemd example.

**Zero reported findings is not a security verdict.** Disabled scanners, unsupported sources, denied permissions, inaccessible reports, unknown coverage, and failures have separate states. Failures retain previous complete findings. Findings absent from a complete response retain their last native status and are labelled “not returned”; absence does not resolve them.

## Provider requirements and limits

Start with GitHub fine-grained tokens granting repository **Metadata: read**, **Dependabot alerts: read**, **Code scanning alerts: read**, and **Secret scanning alerts: read**, limited to selected repositories. Fine-grained tokens have one resource owner; use separate connections for personal and organization repositories. The account also needs permission to view the corresponding alerts, organization approval/SSO where required, and applicable security feature eligibility. Optional default-setup metadata requires **Administration: read**; it is not required for alert import.

For GitLab, use **read_api** and a role permitted to view project vulnerability reports (normally Developer or higher, or an applicable custom security role). Ultimate licensing alone neither configures scanners nor guarantees access. The app probes the authenticated instance's schema and rejects incomplete/inaccessible reports. Configure your actual host only in the GUI/private runtime configuration.

GitHub's dedicated API categories and GitLab report categories differ. Some IaC/container/DAST findings on GitHub are SARIF code alerts; GitLab IaC may be reported under SAST. GitLab's persistent vulnerability report is not a complete history of branch/MR pipeline findings. Unknown analysis times remain unknown. See [provider behavior and setup guidance](docs/providers.md) before interpreting results.

## Local operation

macOS data defaults to `~/Library/Application Support/My Code Dashboard`; Linux defaults to the user config directory plus `My Code Dashboard`. Each installation has its own database, schedule, selections, and Keyring credential namespace. With provisioned credential files, give each installation its own private credential directory; reusing a directory and connection ID shares that secret reference. Use `--data-dir` and a different `--addr` port for another installation. There is no synchronization between Macs.

In **Settings**, install start at login to keep a per-user macOS agent running while logged in. It copies the binary into the private data directory. Stop the foreground process after installation so the agent can take over. A sleeping, powered-off, or logged-out Mac cannot refresh; the next start/wake performs one catch-up for the most recent due day. Removing start at login takes effect at logout; the current agent keeps running for that login session.

Backups include private report data and provider configuration but exclude credentials. Store them privately. See [operations](docs/operations.md) for startup, update, backup, restore, headless Linux, and migration instructions. An optional [container image](docs/docker.md) packages the headless Linux setup with host networking; on macOS, prefer the native app (see [native or Docker](docs/docker.md#native-or-docker)). Loopback binding is enforced. Authentication and an internet-facing deployment are future work; a reverse proxy must not expose the app without an explicitly designed access boundary. Obtain permission before moving corporate reports to a personal server.

## Development

```sh
make check
```

Tests use synthetic fixtures and local HTTP servers. They do not contact real providers or write to Keychain/LaunchAgents. Browser assets are pinned and vendored; edit plain CSS and JavaScript directly. The Alpine CSP build works with the strict Content Security Policy without `unsafe-eval`. See [architecture](docs/architecture.md), [contribution guidance](CONTRIBUTING.md), and [validation](docs/validation.md).

The workflow adapted from ocms-go adds uncached race tests, golangci-lint, vulnerability checks, an exact staged-file check (`make commit-check`), and a pre-push guard (`make install-test-gate`). See [testing and Codex/Codex Security review](docs/testing.md) for the initial-commit requirements and the separate GitHub CI/PR checks.

PR validation also checks shell scripts, workflows, secrets, cross-target packages and CodeQL analysis. See [the GitHub check matrix and first application PR](docs/github-validation.md). Local `make pr-check` additionally requires ShellCheck; it scans only the staged public export. `make package-check` builds and verifies archives without publishing them.

Coding agents should read [AGENTS.md](AGENTS.md); [CLAUDE.md](CLAUDE.md) points Claude Code to the same project instructions.

For direct Go commands, set `GOTOOLCHAIN=go1.26.10` or select that SDK in your IDE. The minimum version in `go.mod` rejects older compilers but does not force a newer installed Go to use an older compiler. Go 1.27 has reproduced TLS handshake timeouts against a self-managed GitLab endpoint; a 1.26.9 build connected with TLS 1.3 and certificate verification enabled, and the pin now selects the 1.26.10 `crypto/x509` security patch. See [why Go 1.26.10, why Go 1.17 cannot build this project, and how to revisit the 1.27 pin](docs/go-toolchain.md), plus [connection troubleshooting](docs/operations.md#provider-connection-troubleshooting).

Build distribution archives without publishing:

```sh
bash scripts/release.sh 0.2.0-dev
```

This builds from the reviewed [public file manifest](scripts/release-files.txt) and prepares macOS/Linux arm64/amd64 archives and checksums under ignored `dist/`. Unlisted local files are excluded from both builds and archives. It never creates a Git tag or GitHub release. Release notes live in [CHANGELOG.md](CHANGELOG.md); a release is cut as a GitHub draft from `main`, and publishing the draft creates its tag. [Repository metadata and publication](docs/repository-creation.md) records the public repository and the publication workflow.

## License

Original application code: **GPL-3.0-only**, copyright 2026 Oleg Ivanchenko and contributors. See [LICENSE](LICENSE). Third-party code keeps its original license; see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). Packaging generates the applicable full notices inside each binary archive, without a checked-in license inventory. Distributed binaries must be accompanied by the corresponding source under GPLv3 section 6; release preparation includes a source archive.
