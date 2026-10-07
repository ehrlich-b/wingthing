#!/bin/sh
# Roost policy pins need namespaces, so a skipped or unmatched test is a
# failure, not a pass. The Docker battery runs this as root; the integration
# gate runs it as the non-root runner with unprivileged user namespaces, the
# way a production roost runs.
set -e
out=$("$1" -test.v -test.timeout 120s -test.run '^TestRoostPolicy(LinuxSiblingRolesStartWithPins|LinuxBrowserCanaryLayoutStarts|BlocksLinuxRootReplacement|LinuxMissingPaths|LinuxPersonalDefaultWithoutEggYAMLStarts)$' 2>&1) || { echo "$out"; exit 1; }
echo "$out"
if echo "$out" | grep -q -- '--- SKIP'; then echo "required Linux roost pin test skipped"; exit 1; fi
echo "$out" | grep -q -- '--- PASS' || { echo "no roost pin test matched"; exit 1; }
