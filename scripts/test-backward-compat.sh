#!/bin/sh
set -eu

BASELINE_REF="${WT_COMPAT_BASELINE_REF:-v0.144.1}"
REPO_ROOT=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
cd "$REPO_ROOT"

if ! git cat-file -e "$BASELINE_REF^{commit}" 2>/dev/null; then
    echo "compatibility baseline $BASELINE_REF is unavailable; fetch tags before running this gate" >&2
    exit 1
fi

COMPAT_TMP_PARENT="${WT_COMPAT_TMPDIR:-/tmp}"
TMP_ROOT=$(mktemp -d "$COMPAT_TMP_PARENT/wtc.XXXXXX")
BASELINE_TREE="$TMP_ROOT/baseline"
STATE_DIR="$TMP_ROOT/state"
AGENT_DIR="$TMP_ROOT/bin"
BASELINE_BIN="$TMP_ROOT/wt-baseline"
CANDIDATE_BIN="$TMP_ROOT/wt-candidate"
CHECK_BIN="$TMP_ROOT/compatcheck"
SERVER_PID=""
WING_PID=""
SESSION_ID=""

cleanup() {
    status=$?
    trap - EXIT HUP INT TERM
    if [ -n "$SESSION_ID" ] && [ -x "$CANDIDATE_BIN" ]; then
        WINGTHING_DIR="$STATE_DIR" "$CANDIDATE_BIN" session kill "$SESSION_ID" >/dev/null 2>&1 || true
    fi
    if [ -n "$WING_PID" ]; then
        kill "$WING_PID" >/dev/null 2>&1 || true
        wait "$WING_PID" 2>/dev/null || true
    fi
    if [ -n "$SERVER_PID" ]; then
        kill "$SERVER_PID" >/dev/null 2>&1 || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    if [ "$status" -eq 0 ]; then
        case "$TMP_ROOT" in
            "$COMPAT_TMP_PARENT"/wtc.*) rm -rf "$TMP_ROOT" ;;
        esac
    else
        echo "compatibility artifacts preserved at $TMP_ROOT" >&2
    fi
    return "$status"
}
trap cleanup EXIT HUP INT TERM

mkdir -p "$BASELINE_TREE" "$STATE_DIR" "$AGENT_DIR"
git archive "$BASELINE_REF" | tar -x -C "$BASELINE_TREE"
mkdir -p "$BASELINE_TREE/web/dist"
printf '%s\n' '<!doctype html><meta charset="utf-8"><title>compatibility fixture</title>' > "$BASELINE_TREE/web/dist/index.html"

(
    cd "$BASELINE_TREE"
    go build -buildvcs=false -ldflags "-X main.version=$BASELINE_REF" -o "$BASELINE_BIN" ./cmd/wt
)
go build -buildvcs=false -ldflags "-X main.version=compat-candidate" -o "$CANDIDATE_BIN" ./cmd/wt
go build -buildvcs=false -o "$CHECK_BIN" ./test/compat
go build -buildvcs=false -o "$AGENT_DIR/claude" ./test/web/canary-agent

echo "== immutable historical migrations =="
git ls-tree -r --name-only "$BASELINE_REF" -- internal/store/migrations internal/relay/migrations |
while IFS= read -r migration; do
    if [ ! -f "$migration" ]; then
        echo "candidate removed historical migration $migration" >&2
        exit 1
    fi
    if ! git show "$BASELINE_REF:$migration" | cmp -s - "$migration"; then
        echo "candidate rewrote historical migration $migration" >&2
        exit 1
    fi
done

echo "== HTTP and WebSocket route surface =="
grep -R -h 's\.mux\.Handle' "$BASELINE_TREE/internal/relay"/*.go |
    sed -n 's/.*HandleFunc("\([^"]*\)".*/\1/p; s/.*Handle("\([^"]*\)".*/\1/p' |
    sort -u > "$TMP_ROOT/baseline-routes.txt"
grep -h 's\.mux\.Handle' internal/relay/*.go |
    sed -n 's/.*HandleFunc("\([^"]*\)".*/\1/p; s/.*Handle("\([^"]*\)".*/\1/p' |
    sort -u > "$TMP_ROOT/candidate-routes.txt"
if [ ! -s "$TMP_ROOT/baseline-routes.txt" ] || [ ! -s "$TMP_ROOT/candidate-routes.txt" ]; then
    echo "route-surface extraction produced an empty result" >&2
    exit 1
fi
removed_routes=$(comm -23 "$TMP_ROOT/baseline-routes.txt" "$TMP_ROOT/candidate-routes.txt")
if [ -n "$removed_routes" ]; then
    echo "candidate removed baseline routes:" >&2
    printf '%s\n' "$removed_routes" >&2
    exit 1
fi

echo "== CLI command and flag surface =="
"$CHECK_BIN" cli "$BASELINE_BIN" "$CANDIDATE_BIN"

echo "== task store upgrade and rollback =="
WINGTHING_DIR="$STATE_DIR" "$BASELINE_BIN" init >/dev/null
WINGTHING_DIR="$STATE_DIR" "$BASELINE_BIN" run "legacy compatibility sentinel" --no-run --unsandboxed >/dev/null
candidate_timeline=$(WINGTHING_DIR="$STATE_DIR" "$CANDIDATE_BIN" timeline)
printf '%s\n' "$candidate_timeline" | grep -F "legacy compatibility sentinel" >/dev/null
WINGTHING_DIR="$STATE_DIR" "$CANDIDATE_BIN" run "current compatibility sentinel" --no-run --unsandboxed >/dev/null
baseline_timeline=$(WINGTHING_DIR="$STATE_DIR" "$BASELINE_BIN" timeline)
printf '%s\n' "$baseline_timeline" | grep -F "legacy compatibility sentinel" >/dev/null
printf '%s\n' "$baseline_timeline" | grep -F "current compatibility sentinel" >/dev/null

start_gateway() {
    binary=$1
    log_path=$2
    PORT=$("$CHECK_BIN" port)
    WINGTHING_DIR="$STATE_DIR" "$binary" relay --local --addr "127.0.0.1:$PORT" >"$log_path" 2>&1 &
    SERVER_PID=$!
    ready=0
    attempt=0
    while [ "$attempt" -lt 100 ]; do
        if ! kill -0 "$SERVER_PID" 2>/dev/null; then
            break
        fi
        if curl --fail --silent --show-error --max-time 1 "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; then
            ready=1
            break
        fi
        attempt=$((attempt + 1))
        sleep 0.1
    done
    if [ "$ready" -ne 1 ]; then
        echo "gateway failed to become ready" >&2
        sed -n '1,240p' "$log_path" >&2
        exit 1
    fi
}

stop_gateway() {
    kill "$SERVER_PID" >/dev/null 2>&1 || true
    wait "$SERVER_PID" 2>/dev/null || true
    SERVER_PID=""
}

start_wing() {
    binary=$1
    log_path=$2
    PATH="$AGENT_DIR:$PATH" WINGTHING_DIR="$STATE_DIR" "$binary" daemon start --foreground \
        --roost "http://127.0.0.1:$PORT" --paths "$STATE_DIR" \
        --egg-config "$REPO_ROOT/test/web/egg.yaml" >"$log_path" 2>&1 &
    WING_PID=$!
    wing_id=$(tr -d '[:space:]' < "$STATE_DIR/wing-id")
    registered=0
    attempt=0
    while [ "$attempt" -lt 150 ]; do
        if ! kill -0 "$WING_PID" 2>/dev/null; then
            break
        fi
        wings=$(curl --fail --silent --show-error --max-time 1 "http://127.0.0.1:$PORT/api/app/wings" 2>/dev/null || true)
        case "$wings" in
            *"\"wing_id\":\"$wing_id\""*) registered=1; break ;;
        esac
        attempt=$((attempt + 1))
        sleep 0.1
    done
    if [ "$registered" -ne 1 ]; then
        echo "wing failed to register" >&2
        sed -n '1,300p' "$log_path" >&2
        exit 1
    fi
    SESSION_ID=$("$CHECK_BIN" pty "ws://127.0.0.1:$PORT/ws/pty?wing_id=$wing_id" "$wing_id" "$STATE_DIR")
    WINGTHING_DIR="$STATE_DIR" "$CANDIDATE_BIN" session kill "$SESSION_ID" >/dev/null
    SESSION_ID=""
}

stop_wing() {
    kill "$WING_PID" >/dev/null 2>&1 || true
    wait "$WING_PID" 2>/dev/null || true
    WING_PID=""
}

echo "== create genuine $BASELINE_REF local state =="
start_gateway "$BASELINE_BIN" "$TMP_ROOT/baseline-gateway-initial.log"
stop_gateway
test -s "$STATE_DIR/device_token.yaml"
cp "$STATE_DIR/device_token.yaml" "$TMP_ROOT/legacy-device-token.yaml"

# Additive current wing fields must remain parseable by the old binary during a
# gateway-first rollout. The explicit allow value preserves old relay behavior.
printf '%s\n' \
    'hosted_relay: allow' \
    'direct_mcp:' \
    '  max_sessions: 4' >> "$STATE_DIR/wing.yaml"

echo "== $BASELINE_REF wing -> current gateway, including PTY start =="
start_gateway "$CANDIDATE_BIN" "$TMP_ROOT/candidate-gateway.log"
cmp -s "$TMP_ROOT/legacy-device-token.yaml" "$STATE_DIR/device_token.yaml"
cmp -s "$STATE_DIR/device_token.yaml" "$STATE_DIR/local_device_token.yaml"
start_wing "$BASELINE_BIN" "$TMP_ROOT/baseline-wing.log"
stop_wing
stop_gateway

echo "== current wing -> $BASELINE_REF gateway, including PTY start =="
start_gateway "$BASELINE_BIN" "$TMP_ROOT/baseline-gateway-rollback.log"
start_wing "$CANDIDATE_BIN" "$TMP_ROOT/candidate-wing.log"
stop_wing
stop_gateway

echo "backward compatibility passed against $BASELINE_REF"
