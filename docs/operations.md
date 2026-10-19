# Operations

Run v1 only on a trusted single-user machine, including a dedicated headless machine. There is no application login. Other local accounts and processes can access the loopback dashboard and download backups; private database permissions do not prevent this HTTP access. Shared hosts and internet-facing deployment are unsupported while authentication is deferred. See [the security policy](../SECURITY.md).

## macOS

Build with `make build`, which selects the security-patched Go 1.26.9 toolchain, then run `bin/my-code-dashboard --open`. The default address is `127.0.0.1:7364`; `--addr '[::1]:7364'` is available for IPv6. Public listeners are rejected. `--data-dir '/absolute/private/path'` selects another independent installation; use a different port too. Do not place runtime data in a source checkout.

`--open` starts the platform browser opener in the background. HTTP serving, scheduling and shutdown do not wait for the browser to exit. On Linux, `xdg-open` may stay attached to the browser; if opening fails, use the printed local URL.

HTTP Host validation accepts a loopback IP or `localhost` without an explicit port only when the listener uses port 80. Bracketed IPv6 such as `[::1]` follows the same rule. Other listener ports, including the default 7364, require a matching explicit Host port. Foreign hosts, malformed authorities and empty explicit ports remain rejected; CSRF and cross-origin checks still apply to mutations.

The default directory is `~/Library/Application Support/My Code Dashboard`, protected with mode 0700. SQLite is 0600; the executable sets umask 0077. Keychain services use a random persisted installation ID so installations do not share credentials. A locked/unavailable keychain produces a visible collection error without a plaintext fallback. A native macOS Keychain dialog may require your approval; enter credentials locally.

In Settings, **Install start at login** copies the running executable to the data directory's `bin/`, writes a per-user LaunchAgent, and bootstraps it without sudo. Exit the foreground process to release its installation lock. The agent retries while that lock is busy. It runs only in your login session and can resume collection after sleep. No claim is made that it can wake a sleeping Mac.

The copied executable must finish writing, receive its executable mode, and close successfully before it replaces the installed binary. A failure at any of those steps aborts installation before the plist or agent is changed, retains the previous executable, and attempts to remove the temporary copy. This includes delayed write errors reported only when the file closes; free space or fix the destination filesystem before retrying.

The plist is written to a private temporary file in the same directory and closed successfully before a rename replaces the previous definition. A partial write or close error leaves the previous plist intact, cleans up the temporary file and stops before loading the agent. The binary may already have been updated at this point. This publication is an atomic file replacement, without a guarantee of durability across a power loss.

The generated agent retains the data directory and listener address. Foreground `--credential-dir` and `--no-scheduler` flags are not copied into it; its next start uses the ordinary credential-backend and scheduler defaults. Use saved schedule settings for a persistent scheduling preference.

Updating: back up first, build the new version, remove auto-start in Settings, and log out/in (or unload the known agent with launchctl) to stop the old process. Replace the binary, run the new foreground executable against the same directory, and install start at login again. Never replace an open SQLite database by hand. There is no automatic binary download/update service or code signing yet.

The service command can also install the agent:

```sh
./bin/my-code-dashboard service --action install
```

The label is `org.olegiv.my-code-dashboard.` plus the first eight characters of the installation ID in the settings record. Its plist is under `~/Library/LaunchAgents/`. Removing start at login through the GUI removes the plist; an already loaded agent continues for the current login. To unload immediately, use the exact known label with `launchctl bootout gui/$(id -u)/LABEL` after closing any active requests. Do not run this with a guessed label. The CLI's `service --action remove` removes and unloads the configured installation.

## Provider connection troubleshooting

Category collections with a last success more than 48 hours ago need attention even when their collection and scanner states remain successful. Overview, the repository list and repository details count each affected category once, including when it also has a collection or scanner issue. A fresh category cannot hide another category's staleness behind the repository's latest-success timestamp. Exactly 48 hours is not yet stale; a category with no successful collection keeps its existing coverage status. Portfolio issue and stale totals cover monitored repositories only; paused repositories retain warnings in their own list/detail views. Collection freshness is separate from provider analysis time, and these warnings do not change retained findings or historical activity.

Reported provider analysis older than 48 hours also needs attention, even after a successful recent collection. Each category still counts once when analysis, collection or other coverage reasons overlap. Exactly 48 hours, future analysis timestamps and unreported analysis timestamps do not add a staleness issue; unreported evidence retains its existing coverage status. The portfolio's **stale category collections** counter continues to count collection age only, while its attention total includes both collection and analysis freshness. Reading the dashboard does not change stored timestamps or trigger a provider scan.

Use the project build commands so the binary uses Go 1.26.9, also the module's minimum compiler version. A reproduced TLS handshake timeout with Go 1.27 disappeared with 1.26.9 while retaining TLS 1.3 and system certificate verification. The compiler pin applies to Make targets, CI, and release archives. Direct commands and IDE builds must also select Go 1.26.9; the module minimum does not downgrade a newer compiler.

[Toolchain decision](go-toolchain.md) documents the support/security rationale, concrete incompatibility with Go 1.17, observed Go 1.27 behavior and its limits, exact pin enforcement, and the verification needed before an upgrade.

Connection timeouts and certificate verification failures have separate messages. For certificate failures, check the configured HTTPS hostname, certificate validity/chain, and OS trust store or corporate CA installation. For timeouts, check VPN/network reachability and the compiler used to build the executable (`go version -m bin/my-code-dashboard`). Provider authentication is checked after TLS succeeds. Credentials should be entered locally again after a failed validation; they are not saved by that failed attempt.

GitLab discovery treats hostname case and an explicit HTTPS port 443 as equivalent for repository identity. Rediscovery through either spelling retains the existing repository ID, selection, credential connection and report history. Different nondefault ports or installation subpaths remain separate instances. This comparison does not rewrite saved connection URLs or relax the matching recipient required when reconnecting a credential.

If an older installation already contains duplicate records for the same instance and numeric project ID, discovery stops without changing repositories or merging their reports. Back up first and select only one duplicate for monitoring to avoid inflated portfolio totals. Reconciliation of existing conflicting histories requires a separate reviewed repair; this update prevents new alias duplicates and preserves a single legacy record, without deleting historical data.

## Backup and restore

Use **Download backup** while running. The archive contains a consistent `VACUUM INTO` database snapshot and manifest, including selections/settings/imported findings. It excludes tokens and provisioned credential files. Backups contain private information and are not encrypted by this app. The manifest identifies the application version and states credentials are excluded.

Deselecting a repository pauses collection and removes it from portfolio totals. Its detail page and explicit repository findings filter retain the last imported active findings; reselect it to resume refreshes. Activity is ordered by actual timestamps, and retention preserves records exactly at the cutoff, including fractional seconds and stored timezone offsets.

If a connection is updated or its credential is removed while discovery is running, that discovery is discarded with a retry message. Run discovery again using the current connection; the older response cannot replace the newly validated identity or import its stale repository list.

With the application stopped:

```sh
./bin/my-code-dashboard backup --output /private/backup/dashboard.backup.tar.gz
./bin/my-code-dashboard restore --archive /private/backup/dashboard.backup.tar.gz
```

Use the same `--data-dir` on each command for a custom installation. Existing backup paths are not overwritten. If writing or closing a new CLI backup fails, the command returns an error and attempts to remove its output so the same path can be retried.

Restore takes the installation lock and validates the compressed archive, its entries, database schema, connection identities, all application records, settings, relationships and integrity before touching the destination. Unsupported or manually altered database schemas, extra triggers/views, mismatched or non-text connection keys, duplicate effective connection identities, malformed/null or incorrectly typed records, mismatches between decoded identities/indexed fields and SQLite columns, traversal, duplicate entries and oversized archives are rejected. Each connection must name GitHub or GitLab and have a valid HTTPS base URL without user information, query or fragment; GitHub's base must be canonical `https://github.com` after normal URL validation. This applies even to connections with no repositories. Nonempty finding and repository links must use HTTPS without user information and match their repository connection's origin (canonical `https://github.com` for GitHub, the configured instance for GitLab); off-provider links are rejected. Empty links are allowed only after the connection itself passes validation. History is validated beyond the UI's display limits. Only backups using this application's supported schema are accepted; restore does not migrate arbitrary SQLite files. A rejected archive leaves the current database intact.

Extraction must finish copying and close the temporary database successfully before validation or replacement begins. A delayed close error rejects the restore with the existing database and installation identity intact. If copying already failed, its error takes precedence over a later close error. Temporary extraction files are removed on either failure.

Live provider links and restored links use the same origin rule: scheme, case-insensitive hostname and effective port. HTTPS port 443 may be explicit or omitted. Nondefault ports remain distinct, so a configured `https://gitlab.example.com:8443` does not accept links to `https://gitlab.example.com`.

For a valid archive, restore retains the backup's settings, repository selections, findings and history. The destination may be corrupt or use a newer schema: it does not need to open successfully in this version. Existing database files, including SQLite WAL, SHM and rollback-journal sidecars, move into a private mode-0700 `before-restore-*` directory before replacement. Orphaned sidecars are preserved too. Nonregular database or sidecar paths are rejected before replacement. The recovery directory contains the previous files, rather than a newly exported SQLite backup; keep its files together. Reading an existing WAL can update its transient SHM index.

Restore preserves a readable, valid destination installation identity, including settings committed in its WAL. Otherwise it generates a fresh identity. Another installation's credential namespace and LaunchAgent label are never adopted from the archive. If the old identity cannot be recovered, its existing LaunchAgent configuration may need to be removed and reinstalled. Restore does not import credentials and disconnects every imported connection, including where its ID matches an existing destination credential. Reconnect and validate each credential in Connections, or through the headless `connection` command, before discovery or manual/scheduled refresh. This requirement persists after restart.

If moving the old files or installing the replacement fails, restore attempts to put the moved files back. If rollback also fails, the error names the recovery directory containing the remaining files; keep the application stopped while recovering them. These multiple file moves are not a crash-atomic transaction. The current restore format permits a database up to 500 MiB; larger deployments need a reviewed export path.

## Headless Debian/Ubuntu

Use the Linux binary and a directory of private externally provisioned credential files, preferably supplied by systemd `LoadCredential`. A desktop Linux session can instead use the OS Secret Service keyring. There is no automatic switch to plaintext when Secret Service is unavailable.

For a headless installation, provision a synthetic example filename such as `github-personal` in a directory readable only by the service account, mode 0600 per file and 0700 for its directory. Populate it through your secret manager/editor; no token is a CLI argument. While the app is stopped, register its connection metadata:

```sh
my-code-dashboard connection \
  --data-dir /private/path/my-code-dashboard \
  --credential-dir /private/path/credentials \
  --credential-id github-personal \
  --provider github --base-url https://github.com --name 'Personal GitHub'
my-code-dashboard serve \
  --data-dir /private/path/my-code-dashboard \
  --credential-dir /private/path/credentials
```

Then use the GUI to discover/select repositories. A credential ID is also the filename supplied by systemd: 1–100 ASCII letters, digits, dots, underscores or hyphens, starting with a letter or digit. Both provisioning and file reads enforce this rule; paths and encoded separators are rejected. The app automatically honors `CREDENTIALS_DIRECTORY`. Headless tokens are changed/revoked outside the browser app, and no credential is written by its GUI.

[my-code-dashboard.service](examples/my-code-dashboard.service) is a **per-user** systemd example. Customize the binary path, credential entries, and private data directory before enabling it. Provision connection metadata into the same directory first. The example is not installed automatically. To reach a remote loopback installation privately, an SSH tunnel is one option:

```sh
ssh -L 7364:127.0.0.1:7364 user@example.com
```

The SSH tunnel protects the network connection; it does not authenticate clients on the server's loopback interface. Keep the host dedicated to trusted single-user use. Internet-facing operation needs a separate design for authentication, authorization, HTTPS, session management, proxy trust, and access restrictions. This app intentionally enforces loopback and has no user login. Obtain explicit permission before storing corporate findings on a personal server. Do not treat a reverse proxy alone as access control.

## Retention and troubleshooting

Daily refresh defaults to 09:00 Europe/Zurich with DST. The scheduler persists the latest due slot and up to three attempts. A restart/wake catches up once for the most recent due day, not once per missed day. No refresh occurs while the process is stopped. `--no-scheduler` disables background scheduling for that process; manual refresh remains available.

If a refresh reaches its 30-minute budget or is interrupted, the next manual or scheduled attempt starts after the last checkpointed repository, wraps around the selected list and clears progress after a complete traversal. This prevents slow portfolios from repeatedly skipping the same tail. Selection changes are respected; a removed or deselected checkpoint falls back to name order. A crash before a checkpoint can replay the current repository. Progress is local to each installation and does not add scheduled attempts beyond the existing budget or resume inside an individual report's pagination.

If a custom refresh time is skipped by a DST change, it moves forward by that change's duration. If it occurs twice, only the earlier occurrence is used. For example, a Zurich 02:30 schedule runs at 03:30 on the spring transition and the first 02:30 on the autumn transition.

Refresh activity shows completion, partial failure, or interruption. Repository coverage shows per-category access and evidence. Expired tokens are replaced in Connections (or externally for headless mode); discovery is separate from security access. HTTP 404 does not distinguish hidden repositories from unavailable features, so it is not reported as disabled or clean. Rate-limit deadlines suppress subsequent calls on that adapter and extend scheduled retry backoff. Unknown GitLab versions/schema capabilities remain visible prerequisites.

An interrupted first import may have saved only some categories. Overview and repository details show every remaining category as unknown, with no collection or analysis timestamp, until a later refresh establishes its status. The collected categories and retained active findings remain visible. These placeholders describe current coverage and do not add inferred reasons to historical activity.

By default, changes retain 90 days and refresh activity 30 days; both are configurable. Saving preferences immediately prunes expired history. The background loop also prunes at startup and every minute, even when daily synchronization is disabled or a refresh is active. This cleanup makes no provider requests. Process downtime, sleep and `--no-scheduler` pause background cleanup; saving preferences or finishing a manual refresh still prunes. Events exactly at the retention cutoff remain. Existing backup archives are snapshots and are not retroactively pruned; manage their retention separately.

Activity shows the latest 200 finding changes for currently monitored repositories; the selection filter is applied before the limit. Overview uses the newest eight of those events. Deselected repositories retain their history within the configured retention window and reappear when selected again. Findings and unselected repository data remain stored. Database/WAL/backups need private disk access; use FileVault or encrypted storage. Logs are terse startup/process messages without provider bodies or tokens. LaunchAgent stdout/stderr goes to `service.log` in the private directory; the app does not log findings. System log rotation is an operator concern for abnormal repeated startup errors.

## Restored connections and retry deadlines

Restore also enforces the finding-field allowlist. Unknown properties in a finding's JSON, including added raw responses, source snippets or secret-value fields, reject the entire archive before the destination is replaced. The error does not echo imported field names or values. Supported finding metadata and recognized JSON field spellings remain accepted; genuine backups can be exported and restored again. This validates field names and types, not the truth or sensitivity of text placed in a supported field. Keep backups private and restore only from trusted sources.

Restore rejects repository metadata that the live adapters cannot use: IDs must be canonical positive decimal integers within signed 64-bit range, names must contain non-whitespace text, and the repository provider must match its referenced connection. Matching database columns and JSON alone are insufficient. Validation occurs before replacing the destination database, so rejected imports leave its data and installation identity unchanged. Genuine GitHub/GitLab backups retain selection and findings; supported empty provider links remain accepted.

Restore retains a readable, valid destination identity or generates a fresh one, and marks every imported connection as requiring credential reconnection. Imported connection IDs can match a destination Keychain entry or provisioned filename for a different host. Discovery and both manual/scheduled collection refuse to use those credentials until explicitly validated again. In Connections, select the existing connection first and verify its displayed provider URL before entering a replacement token. Selection updates the token guidance and clears any entered token; the server rejects a provider/URL mismatch before validation. Browser and headless reconnect validate and normalize both saved and submitted URLs before comparing them: accepted surrounding whitespace and a trailing slash do not prevent reconnection, while a different provider, host, port or installation path is still rejected before credential use. Treat imported connection names and destinations as untrusted. Headless installations must repeat the `connection` command with the intended provider URL and provisioned credential ID. Existing reports remain available. Forgetting a restored connection does not delete an unrelated destination credential.

Replacing or removing credentials while a refresh is running discards that connection's outstanding results and skips its remaining repositories for that run. Earlier committed data stays available; activity records **Connection changed during refresh** with the affected repository/category. Other connections continue normally. After reconnecting, use **Refresh** again to import with the current credential. Removing a local credential does not cancel an HTTP request already in flight or revoke the token at the provider.

An outstanding provider retry deadline survives the next daily schedule slot, edited schedule preferences and application restarts, including a deadline received during manual refresh or on the final scheduled retry. Manual refresh does not consume the scheduled attempt budget. The daily attempt budget resets for a new slot, but the scheduler waits until the saved deadline before collecting again. A rate limit on optional GitLab scanner metadata preserves the successfully imported vulnerability report and its unknown scanner state while retaining the backoff deadline.

A headless credential ID stays bound to its original provider and URL. Updates can replace its token/name, but switching provider hosts requires a different credential ID. This prevents old numeric repository IDs and finding history from being queried against a different instance. The target check runs before credential lookup or network access.
