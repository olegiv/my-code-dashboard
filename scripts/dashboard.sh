#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-only
# Start, stop or check the dashboard as a background process on macOS.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/dashboard.sh start [serve flags] | stop | status

  start   Start the dashboard in the background. Extra flags go to serve,
          for example --open or --demo.
  stop    Stop it gracefully.
  status  Show whether it is running.

Environment: MCD_ADDR (default 127.0.0.1:7364), MCD_DATA_DIR (default
~/Library/Application Support/My Code Dashboard), MCD_BIN (default
bin/my-code-dashboard in this checkout).
EOF
}

die() {
  echo "dashboard: $*" >&2
  exit 1
}

# Launchers such as Finder or Shortcuts may supply a minimal or empty PATH.
# Never let an empty PATH turn into the current directory.
PATH="${PATH:+$PATH:}/usr/bin:/bin:/usr/sbin:/sbin"
[[ "$(uname -s)" == Darwin ]] || die 'macOS only; on Linux use docs/examples/my-code-dashboard.service'
umask 077

# Follow symlinks, for example from ~/bin, back to the checkout.
src="${BASH_SOURCE[0]}"
while [[ -L $src ]]; do
  dir="$(cd -P "$(dirname "$src")" && pwd)"
  src="$(readlink "$src")"
  [[ $src == /* ]] || src="$dir/$src"
done
root="$(cd -P "$(dirname "$src")/.." && pwd)"

bin="${MCD_BIN:-$root/bin/my-code-dashboard}"
addr="${MCD_ADDR:-127.0.0.1:7364}"
data_dir="${MCD_DATA_DIR:-$HOME/Library/Application Support/My Code Dashboard}"
host="${addr%:*}"
port="${addr##*:}"
log="$data_dir/dashboard.log"
domain="gui/$(id -u)"

loopback='^(127\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}|\[::1\])$'
if [[ ! $host =~ $loopback || ! $port =~ ^[0-9]{1,5}$ ]] || ((10#$port == 0 || 10#$port > 65535)); then
  die 'MCD_ADDR needs a loopback IP and a fixed port, for example 127.0.0.1:7364'
fi
# The app accepts leading zeros but listens on, and checks Host against, the
# canonical port.
port=$((10#$port))
addr="$host:$port"
url="http://$addr"

healthy() {
  curl --silent --fail --noproxy '*' --output /dev/null --max-time 2 "$url/healthz"
}

# The process listening on exactly MCD_ADDR, if any.
listener_pid() {
  lsof -nP -t -iTCP@"$host":"$port" -sTCP:LISTEN 2>/dev/null | head -n 1 || true
}

proc_name() {
  ps -p "$1" -o comm= 2>/dev/null || echo unknown
}

is_dashboard() {
  local name
  name="$(proc_name "$1")"
  [[ $name == *my-code-dashboard || $name == "$bin" ]]
}

# The value after FLAG in a loaded agent's program arguments.
agent_arg() {
  launchctl print "$domain/$1" 2>/dev/null | awk -v flag="$2" '
    /^\targuments = \{/ { args = 1; next }
    args && /^\t\}/ { exit }
    args { sub(/^\t+/, ""); if (found) { print; exit } if ($0 == flag) found = 1 }'
}

# A loaded start-at-login agent for MCD_ADDR. Ask launchd rather than reading
# plists: removing start at login in Settings deletes the plist but leaves the
# agent loaded until logout.
agent_label() {
  local label agent
  for label in $(launchctl list 2>/dev/null | awk '$3 ~ /^org\.olegiv\.my-code-dashboard\./ { print $3 }'); do
    agent="$(agent_arg "$label" --addr)"
    # Compare ports numerically, since the agent may spell them with leading zeros.
    if [[ ${agent%:*} == "$host" && ${agent##*:} =~ ^[0-9]{1,5}$ ]] && ((10#${agent##*:} == port)); then
      echo "$label"
      return 0
    fi
  done
  return 1
}

agent_log() {
  echo "$(agent_arg "$1" --data-dir)/service.log"
}

# Wait until the dashboard answers. With a PID, stop early if it exits, and
# accept only that process as the listener: a concurrent start may own the port.
wait_healthy() {
  for _ in $(seq 80); do
    if [[ -n $1 ]] && ! kill -0 "$1" 2>/dev/null; then
      return 1
    fi
    if healthy && [[ -z $1 || "$(listener_pid)" == "$1" ]]; then
      return 0
    fi
    sleep 0.25
  done
  return 1
}

start() {
  local arg open_browser=false pid label
  for arg in "$@"; do
    case $arg in
      -addr | -addr=* | --addr | --addr=* | -data-dir | -data-dir=* | --data-dir | --data-dir=*)
        die 'set the address and data directory with MCD_ADDR and MCD_DATA_DIR' ;;
      -open | -open=true | --open | --open=true) open_browser=true ;;
    esac
  done

  pid="$(listener_pid)"
  if [[ -n $pid ]]; then
    if is_dashboard "$pid" && healthy; then
      echo "Already running at $url (pid $pid). To restart it, for example after rebuilding, stop it first."
      if $open_browser; then open "$url"; fi
      return 0
    fi
    die "$addr is in use by $(proc_name "$pid") (pid $pid)"
  fi

  # Never start a second process that would compete with the agent for the
  # installation lock.
  if label="$(agent_label)"; then
    for arg in "$@"; do
      case $arg in
        -open | -open=true | --open | --open=true) ;;
        *) die "the start-at-login agent $label runs this address and ignores serve flags" ;;
      esac
    done
    launchctl kickstart "$domain/$label" >/dev/null || die "could not start the agent $label"
    wait_healthy "" || die "the agent $label is not answering at $url; see $(agent_log "$label")"
    echo "Running at $url (start-at-login agent $label)."
    if $open_browser; then open "$url"; fi
    return 0
  fi

  [[ -f $bin && -x $bin ]] || die "no executable at $bin; build it first with: make build"
  # umask 077 makes a new data directory private; the app enforces 0700 too.
  mkdir -p "$data_dir"
  [[ ! -L $log ]] || die "$log must not be a symbolic link"
  # Append: a failed start must not truncate the log of an instance that is
  # already running with this data directory on another address.
  echo "--- start $(date '+%Y-%m-%d %H:%M:%S')" >>"$log"
  nohup "$bin" serve --data-dir "$data_dir" --addr "$addr" "$@" >>"$log" 2>&1 </dev/null &
  pid=$!
  if ! wait_healthy "$pid"; then
    if kill -0 "$pid" 2>/dev/null; then
      echo "dashboard: pid $pid started but is not answering at $url yet. Log ($log):" >&2
    else
      echo "dashboard: the dashboard exited during startup. Log ($log):" >&2
    fi
    tail -n 20 "$log" >&2
    exit 1
  fi
  echo "Running at $url (pid $pid). Log: $log"
}

stop() {
  local pid label
  pid="$(listener_pid)"
  if [[ -n $pid ]] && ! is_dashboard "$pid"; then
    die "$addr is served by $(proc_name "$pid") (pid $pid), not the dashboard; leaving it alone"
  fi

  # KeepAlive would restart a killed agent process, so unload the agent first.
  # A loaded agent may also be waiting to restart with nothing listening.
  if label="$(agent_label)"; then
    launchctl bootout "$domain/$label" || die "could not stop the agent $label"
    echo "Stopped the start-at-login agent $label until your next login."
    echo "To remove it, use Settings or: my-code-dashboard service --action remove"
  fi
  if [[ -z $pid ]]; then
    [[ -n $label ]] || echo "Not running at $url."
    return 0
  fi
  kill -TERM "$pid" 2>/dev/null || true
  # The installation lock is released only when the process exits, which can
  # take longer than closing the listener.
  for _ in $(seq 120); do
    if ! kill -0 "$pid" 2>/dev/null; then
      echo "Stopped."
      return 0
    fi
    sleep 0.25
  done
  die "pid $pid is still shutting down after 30 seconds; check again with: scripts/dashboard.sh status"
}

status() {
  local pid label
  pid="$(listener_pid)"
  if [[ -z $pid ]]; then
    echo "Stopped ($url)."
    return 1
  fi
  if ! is_dashboard "$pid"; then
    echo "$addr is served by another process: $(proc_name "$pid") (pid $pid)."
    return 1
  fi
  if ! healthy; then
    echo "Not answering at $url (pid $pid); it may be starting or stopping."
    return 1
  fi
  if label="$(agent_label)"; then
    echo "Running at $url (pid $pid, start-at-login agent $label). Log: $(agent_log "$label")"
  else
    echo "Running at $url (pid $pid). Log: $log"
  fi
}

command="${1:-}"
[[ $# -eq 0 ]] || shift
case $command in
  start) start "$@" ;;
  stop | status)
    [[ $# -eq 0 ]] || die "$command takes no arguments"
    "$command"
    ;;
  -h | --help | help) usage ;;
  *)
    usage >&2
    exit 2
    ;;
esac
