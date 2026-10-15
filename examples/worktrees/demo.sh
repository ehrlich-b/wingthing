#!/usr/bin/env bash
# A local two-worktree workflow; never uses a caller's repository or wt state.
set -euo pipefail

fail() { echo "worktree demo: $*" >&2; exit 1; }
usage() {
    cat >&2 <<'HELP'
Usage: demo.sh start [scratch-parent]
       demo.sh status ROOT
       demo.sh attach ROOT left|right
       demo.sh cleanup ROOT
Set WT_BIN to an absolute path to a built wt binary before start.
HELP
    exit 2
}

# Deliberately drop provider credentials, remote selectors and caller wt state.
wt() {
    env -i PATH="$PATH" HOME="$root/home" TMPDIR="$root/tmp" \
        TERM="${TERM:-xterm}" LANG=C SHELL=/bin/sh \
        WINGTHING_DIR="$root/s" WINGTHING_PREVIEW_DIR="$root/s" \
        "$binary" "$@"
}

git_local() {
    env -i PATH="$PATH" HOME="$root/home" GIT_CONFIG_NOSYSTEM=1 \
        GIT_CONFIG_GLOBAL=/dev/null GIT_TERMINAL_PROMPT=0 \
        git -c core.hooksPath=/dev/null "$@"
}

load_root() {
    [[ -d "$1" && ! -L "$1" ]] || fail 'ROOT must be a generated directory, not a symlink'
    root=$(cd -- "$1" && pwd -P)
    [[ -f "$root/.worktree-demo" && ! -L "$root/.worktree-demo" ]] || fail 'ROOT is not a worktree demo'
    [[ "$(cat -- "$root/.worktree-demo")" == "$root" ]] || fail 'ROOT ownership marker does not match'
    binary=$(cat -- "$root/wt-bin")
    [[ -x "$binary" ]] || fail "built wt is unavailable: $binary"
}

session_id() {
    python3 - "$root/$1.json" <<'PY'
import json, sys
print(json.load(open(sys.argv[1]))['session'])
PY
}

cleanup() {
    local side id active alive
    active=$(wt session ps --json) || return
    for side in left right; do
        [[ -s "$root/$side.json" ]] || continue
        id=$(session_id "$side") || return
        alive=$(python3 -c 'import json,sys; print(any(s["id"] == sys.argv[1] for s in json.load(sys.stdin)))' "$id" <<< "$active") || return
        if [[ "$alive" == True ]]; then
            wt session kill "$id" >&2 || return
        fi
    done
    # All files, worktrees, branches and state belong to the new synthetic repo.
    # Do not remove anything if a recorded session could not be stopped.
    rm -rf -- "$root"
}

[[ $# -gt 0 ]] || usage
command=$1
shift
case "$command" in
    start)
        [[ $# -le 1 ]] || usage
        for tool in git python3 mktemp; do command -v "$tool" >/dev/null || fail "missing $tool"; done
        binary=${WT_BIN:-wt}
        binary=$(command -v -- "$binary") || fail 'build wt first and set WT_BIN'
        binary=$(cd -- "$(dirname -- "$binary")" && printf '%s/%s\n' "$PWD" "$(basename -- "$binary")")
        parent=${1:-${TMPDIR:-/tmp}}
        parent=$(cd -- "$parent" && pwd -P)
        umask 077
        root=$(mktemp -d "$parent/w.XXXXXX")
        printf '%s\n' "$root" > "$root/.worktree-demo"
        printf '%s\n' "$binary" > "$root/wt-bin"
        mkdir "$root/home" "$root/tmp"
        # Retain diagnostics/state if even cleanup fails during a partial start.
        trap 'rc=$?; if (( rc != 0 )); then echo "worktree demo: start failed in $root" >&2; (set -e; cleanup) || echo "worktree demo: retry cleanup $root" >&2; fi' EXIT
        trap 'exit 130' INT
        trap 'exit 143' TERM
        git_local init -q --template= -b main "$root/repo"
        printf '%s\n' 'Local parallel worktree fixture' > "$root/repo/README.txt"
        git_local -C "$root/repo" add README.txt
        git_local -C "$root/repo" -c user.name='Worktree Demo' -c user.email=demo@example.invalid commit -qm 'Create local worktree fixture'
        for side in left right; do
            git_local -C "$root/repo" worktree add -q -b "$side" "$root/$side"
            # A tiny trusted bootstrap writes different files, then stays as a shell.
            # --unsandboxed keeps this provider-free example usable on ordinary hosts.
            wt terminal --unsandboxed --detach --json --name "$side" --cwd "$root/$side" -- \
                /bin/sh -c 'printf "%s\n" "$1" > result.txt; pwd -P > cwd.txt; printf "worktree=%s\n" "$1"; exec /bin/sh -i' demo "$side" > "$root/$side.json"
            wt session wait "$(session_id "$side")" --contains "worktree=$side" --timeout 10s >&2
        done
        trap - EXIT INT TERM
        printf '%s\n' "$root"
        ;;
    status)
        [[ $# == 1 ]] || usage
        load_root "$1"
        wt session ps --json
        ;;
    attach)
        [[ $# == 2 ]] || usage
        [[ "$2" == left || "$2" == right ]] || fail 'choose left or right'
        load_root "$1"
        wt attach "$(session_id "$2")"
        ;;
    cleanup)
        [[ $# == 1 ]] || usage
        load_root "$1"
        cleanup
        ;;
    *) usage ;;
esac
