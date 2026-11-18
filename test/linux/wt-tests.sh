#!/bin/sh
# Run the compiled cmd/wt tests from /cmd/wt, where the image mirrors the repo
# files they read (testdata and ../../ paths), as go test runs them from the
# package directory. Root ignores the permissions TestRemediationUnwritableHistory
# removes, so it runs as the non-root test account and must pass there.
set -e
tests=$1
nonroot=TestRemediationUnwritableHistory
cd /cmd/wt
"$tests" -test.v -test.timeout 120s -test.skip "^$nonroot\$"
# /root is private to root; give the test account a copy it can execute.
mkdir -m 0755 /tmp/wt-tests-nonroot
cp "$tests" /tmp/wt-tests-nonroot/wt-tests
chmod 0755 /tmp/wt-tests-nonroot/wt-tests
out=$(su - testuser -s /bin/sh -c "cd /tmp && /tmp/wt-tests-nonroot/wt-tests -test.v -test.timeout 120s -test.run '^$nonroot\$'" 2>&1) || { echo "$out"; exit 1; }
echo "$out"
echo "$out" | grep -q -- "--- PASS: $nonroot " || { echo "$nonroot did not pass as a non-root user"; exit 1; }
