# Operations

## macOS

Build with `make build`, which selects the security-patched Go 1.26.9 toolchain, then run `bin/my-code-dashboard --open`. The default address is `127.0.0.1:7364`; `--addr '[::1]:7364'` is available for IPv6. Public listeners are rejected. `--data-dir '/absolute/private/path'` selects another independent installation; use a different port too. Do not place runtime data in a source checkout.

The default directory is `~/Library/Application Support/My Code Dashboard`, protected with mode 0700. SQLite is 0600; the executable sets umask 0077. Keychain services use a random persisted installation ID so installations do not share credentials. A locked/unavailable keychain produces a visible collection error without a plaintext fallback. A native macOS Keychain dialog may require your approval; enter credentials locally.

In Settings, **Install start at login** copies the running executable to the data directory's `bin/`, writes a per-user LaunchAgent, and bootstraps it without sudo. Exit the foreground process to release its installation lock. The agent retries while that lock is busy. It runs only in your login session and can resume collection after sleep. No claim is made that it can wake a sleeping Mac.

The generated agent retains the data directory and listener address. Foreground `--credential-dir` and `--no-scheduler` flags are not copied into it; its next start uses the ordinary credential-backend and scheduler defaults. Use saved schedule settings for a persistent scheduling preference.

Updating: back up first, build the new version, remove auto-start in Settings, and log out/in (or unload the known agent with launchctl) to stop the old process. Replace the binary, run the new foreground executable against the same directory, and install start at login again. Never replace an open SQLite database by hand. There is no automatic binary download/update service or code signing yet.

The service command can also install the agent:

```sh
./bin/my-code-dashboard service --action install
```

The label is `org.olegiv.my-code-dashboard.` plus the first eight characters of the installation ID in the settings record. Its plist is under `~/Library/LaunchAgents/`. Removing start at login through the GUI removes the plist; an already loaded agent continues for the current login. To unload immediately, use the exact known label with `launchctl bootout gui/$(id -u)/LABEL` after closing any active requests. Do not run this with a guessed label. The CLI's `service --action remove` removes and unloads the configured installation.

## Provider connection troubleshooting

Use the project build commands so the binary uses Go 1.26.9, also the module's minimum compiler version. A reproduced TLS handshake timeout with Go 1.27 disappeared with 1.26.9 while retaining TLS 1.3 and system certificate verification. The compiler pin applies to Make targets, CI, and release archives. Direct commands and IDE builds must also select Go 1.26.9; the module minimum does not downgrade a newer compiler.

[Toolchain decision](go-toolchain.md) documents the support/security rationale, concrete incompatibility with Go 1.17, observed Go 1.27 behavior and its limits, exact pin enforcement, and the verification needed before an upgrade.

Connection timeouts and certificate verification failures have separate messages. For certificate failures, check the configured HTTPS hostname, certificate validity/chain, and OS trust store or corporate CA installation. For timeouts, check VPN/network reachability and the compiler used to build the executable (`go version -m bin/my-code-dashboard`). Provider authentication is checked after TLS succeeds. Credentials should be entered locally again after a failed validation; they are not saved by that failed attempt.

## Backup and restore

Use **Download backup** while running. The archive contains a consistent `VACUUM INTO` database snapshot and manifest, including selections/settings/imported findings. It excludes tokens and provisioned credential files. Backups contain private information and are not encrypted by this app. The manifest identifies the application version and states credentials are excluded.

Deselecting a repository pauses collection and removes it from portfolio totals. Its detail page and explicit repository findings filter retain the last imported active findings; reselect it to resume refreshes. Activity is ordered by actual timestamps, and retention preserves records exactly at the cutoff, including fractional seconds and stored timezone offsets.

If a connection is updated or its credential is removed while discovery is running, that discovery is discarded with a retry message. Run discovery again using the current connection; the older response cannot replace the newly validated identity or import its stale repository list.

With the application stopped:

```sh
./bin/my-code-dashboard backup --output /private/backup/dashboard.backup.tar.gz
./bin/my-code-dashboard restore --archive /private/backup/dashboard.backup.tar.gz
```

Use the same `--data-dir` on each command for a custom installation. Existing backup paths are not overwritten. Restore takes the installation lock and validates the compressed archive, its entries, database schema, connection identities, all application records, settings, relationships and integrity before touching the destination. Unsupported or manually altered database schemas, extra triggers/views, mismatched or non-text connection keys, duplicate effective connection identities, malformed/null or incorrectly typed records, mismatches between decoded identities/indexed fields and SQLite columns, traversal, duplicate entries and oversized archives are rejected. History is validated beyond the UI's display limits. Only backups using this application's supported schema are accepted; restore does not migrate arbitrary SQLite files. A rejected archive leaves the current database intact.

For a valid archive, restore keeps the prior database as a private `before-restore-*.sqlite` file and retains settings, repository selections, findings and history. It preserves an existing destination's installation identity or generates a fresh identity for a new destination, so another installation's credential namespace and LaunchAgent label are not adopted. Restore does not import credentials and disconnects every imported connection, including where its ID matches an existing destination credential. Reconnect and validate each credential in Connections, or through the headless `connection` command, before discovery or manual/scheduled refresh. This requirement persists after restart. The current restore format permits a database up to 500 MiB; larger deployments need a reviewed export path.

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

Then use the GUI to discover/select repositories. A credential ID is also the filename supplied by systemd. The app automatically honors `CREDENTIALS_DIRECTORY`. Headless tokens are changed/revoked outside the browser app, and no credential is written by its GUI.

[my-code-dashboard.service](examples/my-code-dashboard.service) is a **per-user** systemd example. Customize the binary path, credential entries, and private data directory before enabling it. Provision connection metadata into the same directory first. The example is not installed automatically. To reach a remote loopback installation privately, an SSH tunnel is one option:

```sh
ssh -L 7364:127.0.0.1:7364 user@example.com
```

Internet-facing operation needs a separate design for authentication, authorization, HTTPS, session management, proxy trust, and access restrictions. This app intentionally enforces loopback and has no user login. Obtain explicit permission before storing corporate findings on a personal server. Do not treat a reverse proxy alone as access control.

## Retention and troubleshooting

Daily refresh defaults to 09:00 Europe/Zurich with DST. The scheduler persists the latest due slot and up to three attempts. A restart/wake catches up once for the most recent due day, not once per missed day. No refresh occurs while the process is stopped. `--no-scheduler` disables background scheduling for that process; manual refresh remains available.

If a custom refresh time is skipped by a DST change, it moves forward by that change's duration. If it occurs twice, only the earlier occurrence is used. For example, a Zurich 02:30 schedule runs at 03:30 on the spring transition and the first 02:30 on the autumn transition.

Refresh activity shows completion, partial failure, or interruption. Repository coverage shows per-category access and evidence. Expired tokens are replaced in Connections (or externally for headless mode); discovery is separate from security access. HTTP 404 does not distinguish hidden repositories from unavailable features, so it is not reported as disabled or clean. Rate-limit deadlines suppress subsequent calls on that adapter and extend scheduled retry backoff. Unknown GitLab versions/schema capabilities remain visible prerequisites.

By default, changes retain 90 days and refresh activity 30 days; both are configurable. Findings and unselected repository data remain stored. Database/WAL/backups need private disk access; use FileVault or encrypted storage. Logs are terse startup/process messages without provider bodies or tokens. LaunchAgent stdout/stderr goes to `service.log` in the private directory; the app does not log findings. System log rotation is an operator concern for abnormal repeated startup errors.

## Restored connections and retry deadlines

Restore keeps the destination installation identity, but marks every imported connection as requiring credential reconnection. Imported connection IDs can match a destination Keychain entry or provisioned filename for a different host. Discovery and both manual/scheduled collection refuse to use those credentials until explicitly validated again. In Connections, select the existing connection first and verify its displayed provider URL before entering a replacement token. Selection updates the token guidance and clears any entered token; the server rejects a provider/URL mismatch before validation. Treat imported connection names and destinations as untrusted. Headless installations must repeat the `connection` command with the intended provider URL and provisioned credential ID. Existing reports remain available. Forgetting a restored connection does not delete an unrelated destination credential.

An outstanding provider retry deadline survives the next daily schedule slot, edited schedule preferences and application restarts, including a deadline received during manual refresh or on the final scheduled retry. Manual refresh does not consume the scheduled attempt budget. The daily attempt budget resets for a new slot, but the scheduler waits until the saved deadline before collecting again. A rate limit on optional GitLab scanner metadata preserves the successfully imported vulnerability report and its unknown scanner state while retaining the backoff deadline.

A headless credential ID stays bound to its original provider and URL. Updates can replace its token/name, but switching provider hosts requires a different credential ID. This prevents old numeric repository IDs and finding history from being queried against a different instance. The target check runs before credential lookup or network access.
