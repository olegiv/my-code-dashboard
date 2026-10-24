# Optional container image

Docker is optional. The native binary remains the primary distribution and needs no container runtime. The image packages the [headless Linux deployment](operations.md#headless-debianubuntu) for a trusted single-user host and does not change the trust model in [the security policy](../SECURITY.md): there is no application login, and every local account and process that can reach the loopback listener can read dashboard data and download backups. This project builds the image locally and does not publish it.

## Native or Docker

On macOS, prefer the native app. The container packages the headless Linux deployment; on a Mac it adds Docker Desktop's VM and gives up the app's macOS integration:

| | Native app | Docker on macOS |
| --- | --- | --- |
| Credentials | macOS Keychain; add and replace tokens in **Connections** | Private token files on disk; changed outside the app, then validated with the `connection` command |
| Corporate CA | macOS trust store | Export and mount the CA ([corporate certificate authority](#corporate-certificate-authority)) |
| Start at login | **Install start at login** in Settings (per-user LaunchAgent) | Docker Desktop must be running; `restart: unless-stopped` restarts the service |
| Resources | One process | Docker Desktop's Linux VM keeps running |
| Networking | Loopback | Requires Docker Desktop host networking |
| Data and backups | `~/Library/Application Support/My Code Dashboard`, included in Time Machine unless excluded | A named volume inside Docker Desktop's disk image, which Time Machine usually does not back up (check with `tmutil isexcluded <path>`); use **Download backup** |
| Isolation | Ordinary user process | Read-only root filesystem, no capabilities, `no-new-privileges` |

Both use the same trust model (loopback only, no login, a trusted single-user machine), and neither refreshes while the Mac sleeps. Use the container on a headless Linux host, or on a Mac when you specifically want its isolation or the same setup as a Linux server. After a toolchain change, validate the build you actually run against your providers; see [the toolchain decision](go-toolchain.md).

### Switching between them

Run only one installation against the same repositories: otherwise both refresh them and call the providers separately. Installations are independent, each with its own database, schedule and credentials.

- **Docker to native:** run `docker compose stop dashboard` (the volume keeps its data), then `make build && ./bin/my-code-dashboard --open`. An existing native data directory continues where it left off; use **Refresh now** to catch up. To move the container's newer history instead, use **Download backup** in the container, stop both, and run `./bin/my-code-dashboard restore --archive <file>`. Then start the native app and reconnect each connection in **Connections**: select it, check its URL and enter its token. Finally enable **Install start at login** in Settings and quit the foreground app.
- **Native to Docker:** follow [restore a backup](#restore-a-backup-including-one-from-a-native-installation), then remove start at login in the native app's Settings and quit it.

`docker compose down --volumes` deletes the container's data; download a backup first if you need its history.

## Networking

The app accepts only loopback listeners and loopback Host headers, so Docker port publishing (`-p`, `ports:`) cannot reach it. The container uses host networking instead: the dashboard listens on the host's `127.0.0.1:7364`, the same address and exposure as the native binary. Do not add published ports, a `0.0.0.0` address or a reverse proxy. Public listeners are rejected, and exposing the app needs a separately designed access boundary.

Linux Docker Engine supports host networking directly. Docker Desktop requires **Settings → Resources → Network → Enable host networking**. Without it, the container serves only inside Docker's Linux VM and `http://127.0.0.1:7364` on the desktop host does not connect.

## Step-by-step setup

Run these commands from the project checkout. Paths under `~/.config/my-code-dashboard-docker` are examples; any private directory outside the checkout works. Later sections explain each part.

1. **Prepare Docker.** Start Docker. On Docker Desktop, enable host networking as described in [Networking](#networking).

2. **Create the private folders.** Create the `ca` folder only if your GitLab uses a company certificate authority:

   ```sh
   mkdir -m 700 -p ~/.config/my-code-dashboard-docker/credentials
   mkdir -m 755 -p ~/.config/my-code-dashboard-docker/ca
   ```

3. **Create the GitHub token file** (skip without GitHub). Create a fine-grained read-only token with the permissions in [GitHub.com](providers.md#githubcom), copy it, then run:

   ```sh
   install -m 600 /dev/null ~/.config/my-code-dashboard-docker/credentials/github-personal
   pbpaste > ~/.config/my-code-dashboard-docker/credentials/github-personal
   pbcopy < /dev/null
   ```

4. **Create the GitLab token file** (skip without GitLab). In GitLab, open your avatar → **Edit profile** → **Access tokens** → **Add new token**. Choose only the **`read_api`** scope and set an expiry date. The account needs Developer or a security-report role on the projects, and security reports need GitLab Ultimate. Copy the token, then run:

   ```sh
   install -m 600 /dev/null ~/.config/my-code-dashboard-docker/credentials/gitlab-work
   pbpaste > ~/.config/my-code-dashboard-docker/credentials/gitlab-work
   pbcopy < /dev/null
   ```

   `pbpaste` and `pbcopy` are macOS commands; on Linux, paste the token into the file with a plain-text editor. Each file name is that connection's credential ID. Check that every file shows `-rw-------`:

   ```sh
   ls -l ~/.config/my-code-dashboard-docker/credentials
   ```

   On Linux, then run `sudo chown -R 65532:65532 ~/.config/my-code-dashboard-docker/credentials`.

5. **Add the company CA** (skip unless GitLab uses one). Save its PEM certificate in the `ca` folder. On a Mac that already trusts it:

   ```sh
   security find-certificate -a -c '<CA name>' -p /Library/Keychains/System.keychain \
     > ~/.config/my-code-dashboard-docker/ca/corporate-root.pem
   openssl x509 -in ~/.config/my-code-dashboard-docker/ca/corporate-root.pem -noout -subject -fingerprint -sha256
   ```

   Compare the fingerprint with the one your IT department publishes. See [corporate certificate authority](#corporate-certificate-authority) for DER files and other details.

6. **Save the settings in `.env`.** Compose reads `.env` from the checkout. It holds only paths, Git ignores it, and it never enters the image build. Remove the `MCD_ADDR` line if no native installation uses port 7364, and the last two lines if you skipped step 5:

   ```sh
   cat > .env <<EOF
   MCD_CREDENTIAL_DIR=$HOME/.config/my-code-dashboard-docker/credentials
   MCD_ADDR=127.0.0.1:7365
   MCD_CA_DIR=$HOME/.config/my-code-dashboard-docker/ca
   COMPOSE_FILE=compose.yaml:compose.ca.yaml
   EOF
   ```

   Write absolute paths: Compose does not expand `~` in `.env`. The unquoted `EOF` above lets the shell fill in `$HOME`.

7. **Build the image:**

   ```sh
   docker compose build
   ```

8. **Register the GitHub connection** while the dashboard is stopped:

   ```sh
   docker compose run --rm dashboard connection --data-dir /data \
     --credential-id github-personal \
     --provider github --base-url https://github.com --name 'Personal GitHub'
   ```

9. **Register the GitLab connection** while the dashboard is stopped. Replace the URL with your instance's HTTPS address, including any installation subpath or nondefault port:

   ```sh
   docker compose run --rm dashboard connection --data-dir /data \
     --credential-id gitlab-work \
     --provider gitlab --base-url https://gitlab.example.com --name 'Work GitLab'
   ```

   Each `connection` command validates the token and prints "Connection saved." On "Provider TLS certificate verification failed", complete steps 5 and 6, then rerun the command. For another token or instance, create another file (step 3 or 4) and run another `connection` command with that file name.

10. **Start the dashboard** and check its address:

    ```sh
    docker compose up -d
    docker compose logs dashboard
    ```

    The log shows `Local: http://127.0.0.1:7365`, or port 7364 without `MCD_ADDR`.

11. **Finish in the browser.** Open that address.
    - In **Repositories**, discover your repositories and save the ones to monitor.
    - Click **Refresh now** to import their existing reports.
    - In **Settings**, review the daily refresh time and retention.

To move an existing installation into the container, restore its backup instead of steps 8 and 9; see [restore a backup](#restore-a-backup-including-one-from-a-native-installation). To add a connection later, or to validate a replaced token, run `docker compose stop dashboard`, update the token file (step 3 or 4), rerun its `connection` command (step 8 or 9), then `docker compose up -d`. For backups and updates, see [backup, restore and updates](#backup-restore-and-updates).

## Build and demo

```sh
make docker-build
docker run --rm --network host my-code-dashboard:local serve --demo
```

Open [http://127.0.0.1:7364](http://127.0.0.1:7364). The demo uses synthetic data in a temporary directory inside the container, accepts no credentials and makes no provider API calls. Exit with Ctrl+C.

Like the release archives, the build compiles only files listed in [the public manifest](../scripts/release-files.txt); add a new source or asset file there before building the image. The build stage uses the pinned Go 1.26.10 image and the release archives' flags: CGO disabled, `-trimpath`, no VCS stamping and the `0.2.0-dev` version unless `VERSION` is overridden. Base images are pinned by digest. The runtime image is distroless `static` with the nonroot user (UID 65532) and no shell. `--open` is unavailable; open the printed URL yourself.

`/usr/share/doc/my-code-dashboard/` in the image contains `LICENSE`, `THIRD_PARTY_NOTICES.md` and a `THIRD_PARTY_LICENSES.txt` generated from the image's executable, as in the release archives. The base image's Debian package notices remain under `/usr/share/doc`. If you distribute an image, also provide its corresponding source, for example the release script's source archive.

## Credentials

Keychain and Secret Service are unavailable in the container, so it uses provisioned credential files only. The GUI cannot write credentials. Create a private directory outside the checkout with mode 0700 and one mode-0600 file per credential ID, populated through your secret manager or editor and never as a command argument. The same [credential ID rules](operations.md#headless-debianubuntu) apply.

On Linux, the container user must own the directory and files:

```sh
sudo chown -R 65532:65532 /private/path/credentials
```

A file readable by group or other users is rejected. A 0600 file owned by another user is rejected with "cannot read provisioned credential". Docker Desktop's file sharing lets the container user read 0600 files owned by your desktop account; the mode check still applies.

### Token files

Create one read-only token per account and store it in a file named after its credential ID:

- **GitHub:** a fine-grained token with the repository permissions listed in [GitHub.com](providers.md#githubcom).
- **GitLab:** in GitLab, open your avatar → **Edit profile** → **Access tokens** → **Add new token**, choose only the **`read_api`** scope and set an expiry date. The account normally needs Developer or a security-report role on the projects, and security reports need GitLab Ultimate. No `api` write scope or administrator access is required; see [self-managed GitLab](providers.md#self-managed-gitlab).

On macOS, copy the token to the clipboard and write it without putting it in a command or shell history:

```sh
install -m 600 /dev/null /private/path/credentials/gitlab-work
pbpaste > /private/path/credentials/gitlab-work
pbcopy < /dev/null
```

On Linux, create the file the same way with `install -m 600 /dev/null`, paste the token in a plain-text editor or write it from your secret manager, then apply the `chown` above. Check the result with `ls -l /private/path/credentials`: every file must show `-rw-------`. Use one file per token, for example `github-personal`, `gitlab-work` and `gitlab-oss`.

## Run with Compose

`compose.yaml` mounts the credential directory read-only at `/credentials`, sets `CREDENTIALS_DIRECTORY` and stores the installation in a named volume at `/data`. Register each connection while the service is stopped, then start it:

```sh
export MCD_CREDENTIAL_DIR=/private/path/credentials
docker compose build
docker compose run --rm dashboard connection --data-dir /data \
  --credential-id github-personal \
  --provider github --base-url https://github.com --name 'Personal GitHub'
docker compose run --rm dashboard connection --data-dir /data \
  --credential-id gitlab-work \
  --provider gitlab --base-url https://gitlab.example.com --name 'Work GitLab'
docker compose up -d
```

Run one `connection` command per token file; `--credential-id` must match the file name. For GitLab, `--base-url` is your instance's HTTPS address, including an installation subpath or nondefault port if it has one, such as `https://example.com/gitlab` or `https://gitlab.example.com:8443`. A credential ID stays bound to its provider and URL, so use a new ID for another instance. Each command prints "Connection saved." If GitLab validation fails with "Provider TLS certificate verification failed", add its CA as described in [corporate certificate authority](#corporate-certificate-authority) and run the command again.

Then discover and select repositories in the GUI. `connection` validates the token with the provider. It needs the installation lock, so it fails with "another application process is already using this data directory" while the service runs; stop it first with `docker compose stop dashboard`. Export `MCD_CREDENTIAL_DIR` in every shell that runs `docker compose` for this project, or keep it in `.env` as in [step 6](#step-by-step-setup).

The volume `my-code-dashboard_dashboard-data` holds the mode-0700 data directory and mode-0600 SQLite database, owned by UID 65532. Docker keeps volumes in its private data root; use encrypted host storage. Do not override the service `user:`, because the volume belongs to UID 65532. Set `MCD_ADDR=127.0.0.1:7365` to use another loopback port, for example beside a native installation.

The service runs with a read-only root filesystem, a private `/tmp`, all capabilities dropped and `no-new-privileges`. `restart: unless-stopped` restarts it after failures and Docker restarts, in place of the macOS-only start at login. Scheduled refreshes and catch-up follow [the native rules](operations.md#retention-and-troubleshooting) and need the container to be running. `docker compose logs dashboard` shows the terse startup messages.

## Corporate certificate authority

The container does not use the macOS Keychain or the host's trust store. It trusts the public certificate authorities in the image's CA bundle. If your provider, usually a self-managed GitLab, uses a certificate from a company CA, validation fails with "Provider TLS certificate verification failed" until you add that CA. Never disable certificate verification instead.

Put the CA certificates in a directory outside the checkout, one or more PEM files (`-----BEGIN CERTIFICATE-----`). Use the root CA, plus any intermediate certificate your server does not send. CA certificates are public, so mode 0644 files in a 0755 directory are fine; UID 65532 must be able to read them. A DER `.cer` file is ignored silently, so convert it first:

```sh
openssl x509 -inform der -in corporate-root.cer -out /private/path/ca/corporate-root.pem
```

On a Mac that already trusts the CA, you can export it from the System keychain. `-c` matches part of the certificate name and exports every match, while `openssl x509` shows only the first certificate in a file. Keep only the certificates you intend to trust, and compare each fingerprint with the one your IT department publishes:

```sh
security find-certificate -a -c 'Example Corporate Root CA' -p /Library/Keychains/System.keychain \
  > /private/path/ca/corporate-root.pem
openssl x509 -in /private/path/ca/corporate-root.pem -noout -subject -fingerprint -sha256
```

Enable the `compose.ca.yaml` overlay for every Compose command, including `connection`, which validates over TLS:

```sh
export MCD_CREDENTIAL_DIR=/private/path/credentials
export MCD_CA_DIR=/private/path/ca
export COMPOSE_FILE=compose.yaml:compose.ca.yaml
docker compose up -d --build
```

The overlay mounts the directory read-only at `/corporate-ca` and sets `SSL_CERT_DIR=/etc/ssl/certs:/corporate-ca`. Go still loads the image's public bundle, so GitHub and other public hosts stay trusted, and it adds every PEM certificate in those directories. Do not set `SSL_CERT_FILE`: it replaces the public bundle. With plain `docker run`, add `--volume /private/path/ca:/corporate-ca:ro --env SSL_CERT_DIR=/etc/ssl/certs:/corporate-ca`.

Certificates load when the process starts, so run `docker compose restart dashboard` after changing them. Each added CA is trusted for all of the app's HTTPS connections in that container. Add only CAs your organization operates; a TLS-inspecting proxy's CA lets that proxy read tokens and reports in transit.

## Backup, restore and updates

Use **Download backup** in the GUI while the service runs. For the CLI commands, stop the service and mount a private directory that UID 65532 can write (backup) or read (restore); on Linux, it must own that directory and archive:

```sh
docker compose stop dashboard
docker compose run --rm -v /private/backup:/backup dashboard \
  backup --data-dir /data --output /backup/dashboard.backup.tar.gz
docker compose run --rm -v /private/backup:/backup:ro dashboard \
  restore --data-dir /data --archive /backup/dashboard.backup.tar.gz
docker compose up -d
```

### Restore a backup, including one from a native installation

1. Stop the service with `docker compose stop dashboard`. Restore needs the installation lock.

2. Optionally, back up the data the Docker installation already has. Restore also moves the replaced database into a `before-restore-*` directory inside the volume, but a separate archive is easier to restore later:

   ```sh
   mkdir -m 700 -p ~/.config/my-code-dashboard-docker/backups
   docker compose run --rm -v ~/.config/my-code-dashboard-docker/backups:/backup dashboard \
     backup --data-dir /data --output "/backup/pre-restore-$(date -u +%Y%m%dT%H%M%SZ).backup.tar.gz"
   ```

3. Restore the archive. Mounting the single file is enough, and Docker Desktop can read a mode-0600 archive owned by your account; on Linux, UID 65532 must be able to read it. The command prints "Backup restored. Reconnect provider credentials before refreshing."

   ```sh
   docker compose run --rm \
     -v ~/Downloads/my-code-dashboard.backup.tar.gz:/backup/my-code-dashboard.backup.tar.gz:ro \
     dashboard restore --data-dir /data --archive /backup/my-code-dashboard.backup.tar.gz
   ```

4. List the restored connections. Restore disconnects every connection, and the container can reconnect one only through a token file named after its exact ID. Connections created in the GUI have random 32-character IDs. A token file with a different name creates a second, empty connection instead of reconnecting the restored one.

   ```sh
   tmp="$(mktemp -d)"
   tar -xzf ~/Downloads/my-code-dashboard.backup.tar.gz -C "$tmp" dashboard.sqlite
   sqlite3 -header -column "$tmp/dashboard.sqlite" <<'SQL'
   SELECT id, json_extract(data, '$.provider') AS provider,
          json_extract(data, '$.base_url') AS base_url, json_extract(data, '$.name') AS name
   FROM connections;
   SQL
   rm -rf "$tmp"
   ```

5. For each listed connection, create a token file named with its `id`, as in [step 3 or 4](#step-by-step-setup). To reuse a token file you already have, copy it without changing its mode, for example `cp -n -p ~/.config/my-code-dashboard-docker/credentials/gitlab-work ~/.config/my-code-dashboard-docker/credentials/<id>`. Then reconnect it with exactly the listed provider and URL. A different provider or URL is rejected with "this connection belongs to another provider or URL".

   ```sh
   docker compose run --rm dashboard connection --data-dir /data \
     --credential-id <id> --provider <provider> --base-url <base_url> --name '<name>'
   ```

6. Start the service with `docker compose up -d`. Restored findings and history are visible immediately. In **Connections**, every connection should show **Connected** and none should ask you to reconnect. Then use **Refresh now**.

Token files under other names, for example ones created before the restore, are unused afterwards; delete them. When you move from a native installation, stop it, and remove its start at login in its Settings, unless you mean to keep both. Otherwise both installations refresh the same repositories and call the providers separately. Restore keeps the replaced database in a private `before-restore-*` directory inside the volume. See [backup and restore](operations.md#backup-and-restore) for the validation and recovery rules.

To update, back up first, update the checkout, then run `docker compose up -d --build`. The data volume persists across rebuilds and `docker compose down`. `docker compose down --volumes` permanently deletes the installation's data.

## Check the image

`make docker-check` builds the image with BuildKit's Dockerfile checks. It then validates the compose file, the executable's Go 1.26.10, CGO and Linux metadata, and its generated notices, and confirms that a public `--addr` is rejected. It exports a synthetic build context to verify that the `.dockerignore` allowlist excludes runtime data, credentials, backups and unlisted files. Over host networking it smoke-tests the synthetic demo and a fresh data volume: the page loads, a foreign Host header gets HTTP 403, the lock is released, and the container stops cleanly. With synthetic credential files and no network, it confirms that shared, missing and root-owned files are rejected, that a private file owned by UID 65532 passes the file checks, and that a running service blocks a second process. It validates the CA overlay, then runs a synthetic HTTPS provider with a throwaway CA on an internal Docker network: without the mounted CA the connection fails certificate verification, and with it the handshake succeeds and the provider's HTTP 401 is reported. It uses no real credentials, contacts no real provider and pushes nothing. On Docker Desktop it requires host networking. The **PR quality** workflow runs it on Linux.
