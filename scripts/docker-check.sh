#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-only
# Build the optional container image and smoke-test it on the host network.
# Uses synthetic demo data, temporary volumes, synthetic credentials and a
# synthetic TLS provider on an internal network: no real credentials, no real
# provider calls, and nothing is pushed.
set -euo pipefail
export GOTOOLCHAIN=go1.26.10
cd "$(git rev-parse --show-toplevel)"
image=my-code-dashboard:check
container="my-code-dashboard-check-$$"
volume="my-code-dashboard-check-$$"
fixture="my-code-dashboard-check-tls-$$"
network="my-code-dashboard-check-$$"
work="$(mktemp -d)"
cleanup() {
  docker rm --force "$container" "$fixture" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  docker volume rm "$volume" >/dev/null 2>&1 || true
  docker image rm "$image-credentials" "$image-tlsfixture" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
fail() {
  echo "$1" >&2
  exit 1
}

docker build --check .
docker build --build-arg VERSION=0.1.0-dev --tag "$image" .
# --env-file /dev/null and explicit -f keep a local .env or COMPOSE_FILE out of these checks.
MCD_CREDENTIAL_DIR=/nonexistent docker compose --env-file /dev/null -f compose.yaml config --quiet

# Only allowlisted sources may enter the build context; check the rules on a synthetic tree.
context="$work/context"
mkdir "$context"
cp .dockerignore "$context/"
for path in go.mod go.sum LICENSE THIRD_PARTY_NOTICES.md cmd/app/main.go internal/web/server.go scripts/notices/main.go scripts/release-files.txt \
  .env .git/config .local/state bin/app dist/app.tar.gz output/report.txt docs/guide.md scripts/release.sh \
  dashboard.backup.tar.gz internal/data.db internal/data.db-wal internal/web/state.sqlite; do
  mkdir -p "$context/$(dirname "$path")"
  printf 'synthetic\n' > "$context/$path"
done
printf 'FROM scratch\nCOPY . /\n' |
  docker build --output "type=local,dest=$work/context-out" -f - "$context" > "$work/context.log" 2>&1 ||
  { cat "$work/context.log" >&2; fail 'Cannot export the synthetic build context.'; }
(cd "$work/context-out" && find . -type f | LC_ALL=C sort) > "$work/context-files"
printf './%s\n' LICENSE THIRD_PARTY_NOTICES.md cmd/app/main.go go.mod go.sum internal/web/server.go scripts/notices/main.go scripts/release-files.txt |
  LC_ALL=C sort > "$work/context-expected"
if ! diff "$work/context-expected" "$work/context-files" >&2; then
  fail 'The build context does not match the .dockerignore allowlist.'
fi

# The image must carry the pinned release build and its generated notices.
docker create --name "$container" "$image" >/dev/null
docker cp "$container:/usr/local/bin/my-code-dashboard" "$work/my-code-dashboard"
docker cp "$container:/usr/share/doc/my-code-dashboard/THIRD_PARTY_LICENSES.txt" "$work/licenses.txt"
docker rm "$container" >/dev/null
go version -m "$work/my-code-dashboard" > "$work/buildinfo"
head -n 1 "$work/buildinfo" | grep -q ': go1\.26\.10$' || fail 'Image binary was not built with Go 1.26.10.'
grep -q -E '^[[:space:]]+build[[:space:]]+CGO_ENABLED=0$' "$work/buildinfo" || fail 'Image binary was built with CGO enabled.'
grep -q -E '^[[:space:]]+build[[:space:]]+GOOS=linux$' "$work/buildinfo" || fail 'Image binary does not target Linux.'
grep -q 'Go go1.26.10 / LICENSE' "$work/licenses.txt" || fail 'Image notices are missing the Go license.'

# The container must not weaken the loopback-only listener rule.
if output="$(docker run --rm --network none "$image" serve --addr 0.0.0.0:7364 2>&1)"; then
  fail 'A public listener was accepted.'
fi
grep -q 'public listeners are disabled' <<<"$output" || fail "Unexpected listener error: $output"

url=""
run_dashboard() {
  docker run --detach --name "$container" --network host --read-only --tmpfs /tmp \
    --cap-drop ALL --security-opt no-new-privileges:true "$@" >/dev/null
  url=""
  for _ in $(seq 60); do
    url="$(docker logs "$container" 2>&1 | grep -E -o -m 1 'http://127\.0\.0\.1:[0-9]+' || true)"
    if [[ -n $url ]]; then
      return
    fi
    if [[ "$(docker inspect --format '{{.State.Running}}' "$container")" != true ]]; then
      docker logs "$container" >&2
      fail 'The dashboard container exited before serving.'
    fi
    sleep 0.5
  done
  docker logs "$container" >&2
  fail 'Timed out waiting for the dashboard container.'
}
check_dashboard() {
  curl --fail --silent --show-error --max-time 10 "$url/" > "$work/page.html" ||
    fail "Cannot reach $url from this host; Docker Desktop requires host networking (docs/docker.md)."
  grep -q 'My Code Dashboard' "$work/page.html" || fail 'Unexpected dashboard response.'
  status="$(curl --silent --output /dev/null --write-out '%{http_code}' --max-time 10 --header 'Host: example.com' "$url/")"
  [[ $status == 403 ]] || fail "A foreign Host header returned HTTP $status."
}
stop_dashboard() {
  docker stop --time 15 "$container" >/dev/null
  code="$(docker inspect --format '{{.State.ExitCode}}' "$container")"
  docker rm "$container" >/dev/null
  [[ $code == 0 ]] || fail "The dashboard container exited with status $code."
}

run_dashboard "$image" serve --demo --no-scheduler --addr 127.0.0.1:0
check_dashboard
stop_dashboard

# A fresh named volume must initialize, release its lock and reopen.
for _ in 1 2; do
  run_dashboard --volume "$volume:/data" "$image" serve --data-dir /data --no-scheduler --addr 127.0.0.1:0
  check_dashboard
  stop_dashboard
done

# Provisioned credential files must be private and owned by the container user.
# The image layer fixes ownership on every platform; values are synthetic and
# --network none keeps a passing file check from reaching any provider.
mkdir -p "$work/credentials/directory"
printf 'synthetic-not-a-token\n' > "$work/credentials/token"
# Create the directory first: a file COPY would give implicit parents the file mode.
printf '%s\n' "FROM $image" \
  'COPY --chown=65532:65532 --chmod=0700 directory /credentials' \
  'COPY --chown=65532:65532 --chmod=0600 token /credentials/app-owned' \
  'COPY --chmod=0600 token /credentials/root-owned' \
  'COPY --chown=65532:65532 --chmod=0644 token /credentials/shared' |
  docker build --tag "$image-credentials" -f - "$work/credentials" > "$work/credentials.log" 2>&1 ||
  { cat "$work/credentials.log" >&2; fail 'Cannot build the synthetic credential image.'; }
connection_network=none
connection_url=https://gitlab.invalid
# expect_connection ID MESSAGE [DOCKER-RUN-OPTIONS...]: the headless connection must fail with MESSAGE.
expect_connection() {
  local id="$1" message="$2"
  shift 2
  if output="$(docker run --rm --network "$connection_network" --read-only --tmpfs /tmp --cap-drop ALL \
    --security-opt no-new-privileges:true --env CREDENTIALS_DIRECTORY=/credentials "$@" \
    --volume "$volume:/data" "$image-credentials" connection --data-dir /data --credential-id "$id" \
    --provider gitlab --base-url "$connection_url" --name Synthetic 2>&1)"; then
    fail "Synthetic credential $id was accepted by a synthetic provider."
  fi
  grep -q "$message" <<<"$output" || fail "Unexpected result for synthetic credential $id: $output"
}
expect_connection shared 'accessible to other users'
expect_connection missing 'credential file is missing'
expect_connection root-owned 'cannot read provisioned credential'
expect_connection app-owned 'Cannot reach provider'

# A running service holds the installation lock against a second process.
run_dashboard --volume "$volume:/data" "$image" serve --data-dir /data --no-scheduler --addr 127.0.0.1:0
expect_connection app-owned 'another application process is already using this data directory'
stop_dashboard

# The CA overlay must add a corporate CA through SSL_CERT_DIR and require its directory.
ca_cert_dir=/etc/ssl/certs:/corporate-ca
MCD_CREDENTIAL_DIR=/nonexistent MCD_CA_DIR=/nonexistent docker compose --env-file /dev/null -f compose.yaml -f compose.ca.yaml config > "$work/ca-config.yaml"
grep -q "SSL_CERT_DIR: $ca_cert_dir" "$work/ca-config.yaml" || fail 'The CA overlay does not set SSL_CERT_DIR.'
grep -A 1 'target: /corporate-ca' "$work/ca-config.yaml" | grep -q 'read_only: true' ||
  fail 'The CA overlay does not mount /corporate-ca read-only.'
if env -u MCD_CA_DIR MCD_CREDENTIAL_DIR=/nonexistent docker compose --env-file /dev/null -f compose.yaml -f compose.ca.yaml config --quiet 2>/dev/null; then
  fail 'The CA overlay accepted a missing MCD_CA_DIR.'
fi

# A synthetic provider on an internal network presents a certificate from a throwaway CA.
builder="$(sed -n -E 's/^FROM --platform=[^ ]+ ([^ ]+) AS build$/\1/p' Dockerfile)"
runtime="$(sed -n -E 's/^FROM ([^ ]+)$/\1/p' Dockerfile)"
[[ -n $builder && -n $runtime ]] || fail 'Cannot read the Dockerfile base images.'
printf '%s\n' "FROM $builder AS build" 'COPY main.go /src/main.go' \
  'RUN cd /src && CGO_ENABLED=0 go build -trimpath -o /tlsfixture main.go' \
  "FROM $runtime" 'COPY --from=build /tlsfixture /tlsfixture' 'ENTRYPOINT ["/tlsfixture"]' |
  docker build --tag "$image-tlsfixture" -f - scripts/tlsfixture > "$work/tlsfixture.log" 2>&1 ||
  { cat "$work/tlsfixture.log" >&2; fail 'Cannot build the synthetic TLS provider.'; }
docker network create --internal "$network" >/dev/null
docker run --detach --name "$fixture" --network "$network" --network-alias gitlab.ca-check.invalid \
  --read-only --cap-drop ALL --security-opt no-new-privileges:true "$image-tlsfixture" >/dev/null
mkdir -m 0755 "$work/ca"
for _ in $(seq 60); do
  docker logs "$fixture" > "$work/ca/corporate-ca.pem" 2>/dev/null || true
  if grep -q 'END CERTIFICATE' "$work/ca/corporate-ca.pem"; then
    break
  fi
  sleep 0.5
done
grep -q 'END CERTIFICATE' "$work/ca/corporate-ca.pem" || { docker logs "$fixture" >&2; fail 'The synthetic TLS provider did not start.'; }
chmod 0644 "$work/ca/corporate-ca.pem"
connection_network="$network"
connection_url=https://gitlab.ca-check.invalid:8443
expect_connection app-owned 'TLS certificate verification failed'
# HTTP 401 proves the handshake trusted the mounted CA.
expect_connection app-owned 'Credential expired or was rejected' \
  --volume "$work/ca:/corporate-ca:ro" --env "SSL_CERT_DIR=$ca_cert_dir"
echo "Container checks passed for $image (nothing pushed)."
