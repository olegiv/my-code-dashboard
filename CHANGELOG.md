# Changelog

All notable changes to this project are documented in this file. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2026-10-24

### Added

#### Running from source

- `make run` builds the app, starts it and opens the dashboard in the default browser; `ARGS` passes extra flags.
- `make docker-up` starts the optional Compose service, waits until it answers and opens it in the default browser.

#### Command line

- `--help` lists the commands (`serve`, `backup`, `restore`, `connection`, `service`, `version`) above the flags.
- Builds made with `make` include the source commit in their version, for example `0.2.0-dev+f2a5537`, with `-dirty` for uncommitted changes. Release builds keep the plain version.

#### Documentation

- A comparison of the native app and the Docker image, with steps for switching between them. On macOS the native app is recommended.

### Changed

- A plain `make` now lists the available targets instead of building; use `make build`.
- `SECURITY.md` states that only the latest release is supported.

### Fixed

- The native-to-Docker restore steps now stop a start-at-login agent with `my-code-dashboard service --action remove`; removing it in Settings only takes effect at logout.

## [0.1.0] - 2026-10-24

First release. My Code Dashboard is a local, single-user dashboard that imports existing GitHub and GitLab security reports. It never runs scanners or changes provider state.

### Added

#### Providers

- GitHub repository discovery with Dependabot, code scanning and secret scanning alerts, using read-only fine-grained tokens.
- Self-managed GitLab discovery and schema-aware GraphQL vulnerability reports with native categories and lifecycle states, using a `read_api` token.

#### Dashboard

- Portfolio and repository views with severity, category, provider, repository, group/owner, native status and text filters.
- Coverage that keeps collection time, analysis evidence, gaps, stale evidence and collection failures apart. Zero reported findings is never presented as a security verdict.
- Change history against the previous successful import, and refresh activity with per-repository and per-category reasons.

#### Operation

- Daily 09:00 Europe/Zurich refresh with daylight-saving handling, manual refresh, missed-run catch-up, bounded retries and overlap prevention.
- Credentials in the macOS Keychain, the Linux Secret Service or private provisioned credential files, with no plaintext fallback.
- Backup export and validated offline restore, macOS start at login, and a Linux systemd example.
- Optional Docker image and Compose setup for headless Linux, with host networking, provisioned credential files and an optional corporate CA overlay (`compose.ca.yaml`).

### Security

- The app listens on loopback addresses only and checks Host headers, CSRF tokens and cross-origin requests under a strict Content Security Policy. It has no login: run it only on a trusted single-user machine.
- Built with Go 1.26.10, which fixes GO-2026-6809 (CVE-2026-111943) in `crypto/x509`.

[Unreleased]: https://github.com/olegiv/my-code-dashboard/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/olegiv/my-code-dashboard/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/olegiv/my-code-dashboard/releases/tag/v0.1.0
