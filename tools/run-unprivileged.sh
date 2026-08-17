#!/bin/bash
#
# run-unprivileged.sh drives launcher end-to-end as an unprivileged user against a local
# mock Kolide server, and samples the state of the osquery extension socket while it runs.
# It exists to reproduce and validate unprivileged-execution problems -- see
# docs/unprivileged-execution.md.
#
# Must be run as root, since it creates the unprivileged user and drops to it.
#
#   sudo ./tools/run-unprivileged.sh [--user kolide] [--root-dir DIR] [--seconds 60]
#
set -euo pipefail

RUNAS=kolide
SECONDS_TO_RUN=60
ROOT_DIR=""
OSQUERYD=""
PORT=9099

while [ $# -gt 0 ]; do
  case "$1" in
    --user) RUNAS="$2"; shift 2 ;;
    --root-dir) ROOT_DIR="$2"; shift 2 ;;
    --seconds) SECONDS_TO_RUN="$2"; shift 2 ;;
    --osqueryd) OSQUERYD="$2"; shift 2 ;;
    --port) PORT="$2"; shift 2 ;;
    *) echo "unknown flag $1" >&2; exit 1 ;;
  esac
done

if [ "$(id -u)" != "0" ]; then
  echo "must run as root -- this script creates an unprivileged user and drops to it" >&2
  exit 1
fi

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK=${WORK:-/opt/launcher-unprivileged}
ROOT_DIR=${ROOT_DIR:-$WORK/root}

if ! id "$RUNAS" >/dev/null 2>&1; then
  echo "==> creating unprivileged user $RUNAS"
  useradd -m -s /bin/bash "$RUNAS"
fi

if [ -z "$OSQUERYD" ]; then
  OSQUERYD=$(command -v osqueryd || true)
fi
if [ -z "$OSQUERYD" ]; then
  echo "no osqueryd found -- pass --osqueryd, or run: go run ./tools/download-osquery.go" >&2
  exit 1
fi

echo "==> building launcher and the mock Kolide server"
mkdir -p "$WORK/bin"
(cd "$REPO_ROOT" && go build -o "$WORK/bin/launcher" ./cmd/launcher)
(cd "$REPO_ROOT" && go build -o "$WORK/bin/mock-kolide-server" ./tools/mock-kolide-server)

rm -rf "$ROOT_DIR"
mkdir -p "$ROOT_DIR"
chown -R "$RUNAS" "$ROOT_DIR"
chmod 0755 "$WORK" "$WORK/bin"

echo "==> starting mock Kolide server on 127.0.0.1:$PORT"
"$WORK/bin/mock-kolide-server" -addr "127.0.0.1:$PORT" > "$WORK/mock.log" 2>&1 &
MOCK_PID=$!
trap 'kill $MOCK_PID 2>/dev/null || true' EXIT
sleep 1

echo "==> starting launcher as $RUNAS (root directory $ROOT_DIR)"
su "$RUNAS" -c "HOME=/home/$RUNAS $WORK/bin/launcher \
  --root_directory=$ROOT_DIR \
  --osqueryd_path=$OSQUERYD \
  --hostname=127.0.0.1:$PORT \
  --insecure_transport --insecure \
  --enroll_secret=mock-secret \
  --autoupdate=false \
  --watchdog_enabled \
  --osquery_healthcheck_startup_delay=15s \
  --debug --osquery_verbose" > "$WORK/launcher.log" 2>&1 &
LAUNCHER_PID=$!

echo "==> sampling for ${SECONDS_TO_RUN}s (socket state / osqueryd process count)"
for i in $(seq 1 "$SECONDS_TO_RUN"); do
  sleep 1
  sockets=$(find "$ROOT_DIR" -maxdepth 1 -name 'osquery-*.sock' 2>/dev/null | wc -l)
  procs=$(pgrep -u "$RUNAS" -x osqueryd 2>/dev/null | wc -l)
  echo "t=${i}s manager_sockets=$sockets osqueryd_procs=$procs"
  # The failure mode we are chasing: osquery removed its own manager socket but is still up.
  if [ "$sockets" = "0" ] && [ "$procs" != "0" ]; then
    echo "    !! manager socket gone while osqueryd is still running"
  fi
done

kill -TERM "$LAUNCHER_PID" 2>/dev/null || true
sleep 3
pkill -u "$RUNAS" -x osqueryd 2>/dev/null || true

echo
echo "==> distributed query results received by the mock server: $(grep -c '^RESULT' "$WORK/mock.log" || true)"
grep '^RESULT' "$WORK/mock.log" | tail -6 || true
echo
echo "logs: $WORK/launcher.log, $WORK/mock.log"
