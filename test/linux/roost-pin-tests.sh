#!/bin/sh
# Roost policy pins need namespaces that userns-restricted hosts only grant
# here, so a skipped or unmatched test is a failure, not a pass.
set -e
run() {
  if [ "$3" = testuser ]; then
    out=$(su - testuser -s /bin/sh -c "'$1' -test.v -test.timeout 120s -test.run '$2'" 2>&1) || { echo "$out"; exit 1; }
  else
    out=$("$1" -test.v -test.timeout 120s -test.run "$2" 2>&1) || { echo "$out"; exit 1; }
  fi
  echo "$out"
  if echo "$out" | grep -q -- '--- SKIP'; then echo "required Linux test skipped: $2"; exit 1; fi
  echo "$out" | grep -q -- '--- PASS' || { echo "no test matched: $2"; exit 1; }
}
pattern='^TestRoostPolicy(LinuxSiblingRolesStartWithPins|BlocksLinuxRootReplacement)$'
run /root/eggclient-tests "$pattern" root
# /root is not searchable by testuser, including for the sandbox's re-execs.
nonroot=$(mktemp /tmp/roost-pin-tests.XXXXXX)
trap 'rm -f "$nonroot"' EXIT
cp /root/eggclient-tests "$nonroot"
chmod 755 "$nonroot"
run "$nonroot" "$pattern" testuser
