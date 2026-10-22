#!/bin/sh
# Run on the host for an existing plain shell session; opt in explicitly.
set -eu
: "${WT_SESSION_ID:?set the full immutable ID from wt session list --json}"
wt_bin=${WT_BIN:-wt}
revision=0
cursor=0
report() {
    revision=$((revision + 1))
    cursor=$(python3 -c 'import json,sys; print(json.dumps({"goal":"Validate release","update":"Shell task " + sys.argv[4],"next_action":"Follow the release checklist","shell":{"session_id":sys.argv[1],"revision":int(sys.argv[2]),"after":int(sys.argv[3]),"state":sys.argv[4]}}))' "$WT_SESSION_ID" "$revision" "$cursor" "$1" |
        "$wt_bin" session report "$WT_SESSION_ID" --json |
        python3 -c 'import json,sys; print(json.load(sys.stdin)["event"]["sequence"])')
}
report working
printf 'Checking release inputs\n'
report waiting
printf 'Waiting for continue; press Enter\n'
IFS= read -r acknowledgement
report continue
printf 'Continuing release checks\n'
report finished
