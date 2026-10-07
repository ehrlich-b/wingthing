#!/bin/sh
# Roost policy pins need namespaces that userns-restricted hosts only grant
# here, so a skipped or unmatched test is a failure, not a pass.
set -e
run() {
  out=$("$1" -test.v -test.timeout 120s -test.run "$2" 2>&1) || { echo "$out"; exit 1; }
  echo "$out"
  if echo "$out" | grep -q -- '--- SKIP'; then echo "required Linux test skipped: $2"; exit 1; fi
  echo "$out" | grep -q -- '--- PASS' || { echo "no test matched: $2"; exit 1; }
}
run /root/eggclient-tests '^TestRoostPolicy(LinuxSiblingRolesStartWithoutPins|BlocksLinuxRootReplacement)$'
