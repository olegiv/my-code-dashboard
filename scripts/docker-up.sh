#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-only
# Start the optional Compose service and open the dashboard in the default browser.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
docker compose up -d dashboard
# The service prints its listener address; this follows a custom MCD_ADDR.
url=""
for _ in $(seq 60); do
  url="$(docker compose logs --no-log-prefix dashboard 2>/dev/null |
    grep -E -o 'http://(127\.[0-9.]+|\[::1\]):[0-9]+' | tail -n 1 || true)"
  if [[ -n $url ]] && curl --silent --output /dev/null --max-time 2 "$url/"; then
    break
  fi
  url=""
  sleep 0.5
done
if [[ -z $url ]]; then
  docker compose logs --tail 20 dashboard >&2 || true
  echo 'The dashboard is not reachable. On Docker Desktop, enable host networking (docs/docker.md).' >&2
  exit 1
fi
echo "Dashboard: $url"
if [[ "$(uname -s)" == Darwin ]]; then
  open "$url"
elif command -v xdg-open >/dev/null 2>&1; then
  xdg-open "$url" >/dev/null 2>&1 &
else
  echo 'Open the address above in your browser.'
fi
