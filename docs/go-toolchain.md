# Why the project uses Go 1.26.9

Decision reviewed on **18 October 2026**. Go **1.26.9** is the module minimum and the tested build toolchain for this project. Go **1.17** cannot build the current application and is outside upstream support. Go **1.27** is a separate comparison: an executable built with that release family encountered a TLS handshake timeout against a self-managed GitLab instance during local troubleshooting. Rebuilding with 1.26.9 restored the connection without bypassing certificate verification.

## Version comparison

| Version | Project status | Reason |
| --- | --- | --- |
| Go 1.17 | Unsupported and incompatible | Missing language/library features and too old for the selected dependencies. |
| Go 1.26.0–1.26.8 | Below the module minimum | Builds require at least the 1.26.9 patch, including upstream security fixes. |
| Go 1.26.9 | Tested and pinned | Supported release family, current dependencies, and the successful local GitLab connection. |
| Go 1.27.x | Requires renewed compatibility testing | A local TLS timeout motivated the pin; this does not establish that every 1.27 patch fails. |

Go 1.26.9 was released on 8 October 2026 with security fixes affecting the Go command and packages including TLS, HTML templates, and HTTP. On the review date, 1.26.9 is the latest patch listed for the 1.26 family; 1.27.2 is also available. Go supports a release family until two newer families exist, so 1.26 remains supported while 1.27 is current. This dated decision must be revisited as patches and support windows change. [Official release history and policy](https://go.dev/doc/devel/release).

## Why Go 1.17 is not an option

Go 1.17 was released in August 2021 and is well outside the upstream support window. It does not receive the current security fixes needed for an application handling access tokens, private reports, HTTPS, and untrusted provider content. [Go 1.17 history](https://go.dev/doc/devel/release#go1.17).

There are also concrete build barriers in this checkout:

- `internal/store/store.go` and `internal/provider/provider.go` use generic functions and `any`, which arrived after Go 1.17. [Go 1.18 language changes](https://go.dev/doc/go1.18#language).
- `internal/web/server.go` uses `slices` and the `min`/`max` built-ins, introduced in Go 1.21. [Go 1.21 release notes](https://go.dev/doc/go1.21).
- The web boundary uses `http.NewCrossOriginProtection`, added in Go 1.25. [Standard library API](https://pkg.go.dev/net/http#NewCrossOriginProtection).
- `go.mod` declares `go 1.26.9`, enforcing the patched project baseline. The selected `modernc.org/sqlite v1.60.1`, `modernc.org/libc v1.77.1`, and `golang.org/x/sys v0.49.0` modules declare Go 1.26.0. The application deliberately requires a later patch than those dependency minimums. These requirements were checked from the actual module graph, rather than inferred from dependency names.

Changing the version number in `go.mod` would not backport the language features, standard library, or dependencies. Go's compatibility promise primarily lets newer toolchains build older compatible programs; it does not make old compilers understand newer programs. [Go compatibility policy](https://go.dev/doc/go1compat).

## Why retain 1.26.9 instead of immediately using 1.27

The pin records an observed operational compatibility issue:

1. The provider connection failed before authentication, with a TLS handshake timeout in a Go 1.27 build.
2. The Go 1.26.9 build connected to the same self-managed GitLab service. The successful connection retained TLS 1.3 and normal certificate validation.
3. The local connection was then validated and saved through the app's normal credential workflow.

These are observations from local troubleshooting, not an upstream bug diagnosis or a public reproducer. A timeout does not identify whether the cause is Go, the server, a proxy/VPN, or another network intermediary. The public project deliberately excludes the corporate hostname, credentials, and private diagnostics. It does not claim that all GitLab installations or all 1.27 patches are affected. The earlier failure is not being re-tested by the automated suite.

Rebuilding a Go application changes its embedded runtime and standard library, including the HTTPS client, even when application source is unchanged. Go's source compatibility policy allows security fixes and bug fixes to change behavior. Release notes describe TLS changes in both [Go 1.26](https://go.dev/doc/go1.26#crypto/tls) and [Go 1.27](https://go.dev/doc/go1.27#crypto/tls); those notes alone do not identify the cause of this timeout.

The workaround is therefore a supported, patched, tested compiler selection. There is no `InsecureSkipVerify`, forced legacy cipher suite, TLS downgrade, or application-wide `GODEBUG` workaround. Certificate failures still require fixing the hostname, certificate chain, or system/corporate CA trust. See [connection troubleshooting](operations.md#provider-connection-troubleshooting).

## How the pin is enforced

| Location | Effect |
| --- | --- |
| `go.mod`: `go 1.26.9` | Rejects older compilers; automatic selection may obtain a sufficiently new toolchain. |
| `Makefile`: `GOTOOLCHAIN=go1.26.9` | Selects the exact toolchain for build, tests, and checks. |
| `scripts/release.sh` | Explicitly selects 1.26.9 for local and CI archive builds. |
| `.github/workflows/ci.yml` and `release.yml` | Install and explicitly select 1.26.9. |
| Direct commands and GoLand | Must explicitly select 1.26.9 too. |

The minimum is not an exact lock: with automatic selection, a newer installed Go can remain selected. A separate `toolchain go1.26.9` line would be redundant with this minimum and is omitted by Go's module tooling; it would not force a downgrade either. `GOTOOLCHAIN=go1.26.9`, without `+auto`, selects that exact toolchain, downloading and verifying it if necessary. With automatic selection disabled, an older compiler rejects the module. Go 1.17 predates this mechanism; install 1.26.9 directly or use a Go 1.21+ launcher. [Official toolchain selection documentation](https://go.dev/doc/toolchain).

Verify the selected compiler and the compiler embedded in the executable separately:

```sh
GOTOOLCHAIN=local go version
GOTOOLCHAIN=go1.26.9 go version
make build
GOTOOLCHAIN=go1.26.9 go version -m bin/my-code-dashboard
```

The second command and the executable's build metadata should report `go1.26.9`. In GoLand, select the 1.26.9 project SDK and set `GOTOOLCHAIN=go1.26.9` in direct Go run/test configurations. A prebuilt application needs no Go installation; upgrading the Go installed on a Mac does not change an already built binary. Updating the runtime requires rebuilding and replacing that binary.

The module requirements can be inspected without using credentials or contacting provider APIs:

```sh
GOTOOLCHAIN=go1.26.9 go list -m -f '{{.Path}} {{.Version}} go={{.GoVersion}}' all
```

## Updating or removing the pin

Treat the pin as a compatibility measure, not a permanent exemption from security updates. Review later 1.26 security patches promptly. Move to a newer supported release after verifying the affected provider path; do not retain an unsupported release family indefinitely.

Before changing the selected toolchain:

1. Record the exact current and candidate patch versions. Compare them on the same network, VPN/proxy settings, system trust store, and approved HTTPS endpoint. Keep private hostnames and diagnostics out of public artifacts. Check basic TLS reachability before involving credentials.
2. With the user's authorization for live validation, test connection validation and read-only report collection against the affected provider. Keep TLS/certificate verification enabled and confirm invalid certificates are still rejected. A successful public GitHub connection alone does not exercise the previously failing path.
3. Run the synthetic race tests, vet, module verification, dependency vulnerability checker, and relevant HTTP/security tests under the candidate toolchain. Automated tests must continue to avoid live providers and real credentials.
4. Build macOS/Linux arm64/amd64 binaries, inspect their compiler metadata, and verify the native executable and distribution contents. Local cross-compilation does not establish Linux runtime or keyring behavior.
5. Update the module's patched minimum and the exact build selection consistently in `go.mod`, Makefile, both workflows, release script, README, operations/contribution guidance, and agent instructions. Rebuild release archives with the selected compiler and review their generated third-party notices; keep generated notices out of the repository.
6. Record the normal/failure results, tested tree, and remaining limits. Replace the running executable only within the user's authorized update workflow; stop at any existing commit/push/publication approval boundary.

For now, the passing local build, synthetic checks, and archive verification establish 1.26.9 as the tested project baseline. They do not establish a root cause for the historical 1.27 timeout or guarantee that a later security release will behave identically.
