#!/bin/sh
set -e
mkdir -p /opt/wingthing/eng /opt/wingthing/support /opt/wingthing/product /opt/wingthing/sales
echo "canary marker $(date -u)" > /opt/wingthing/eng/README.txt
echo "support marker $(date -u)" > /opt/wingthing/support/README.txt
# Member sessions require a per-path egg.yaml (v0.48 folder-ACL design); the
# Slide ansible role installs one per role dir. Use the trusted-container
# policy here since the docker container is the boundary.
cp /root/.wingthing/egg.yaml /opt/wingthing/eng/egg.yaml
cp /root/.wingthing/egg.yaml /opt/wingthing/support/egg.yaml

/usr/local/bin/wt roost start --foreground --audit --addr :8080 &
ROOST_PID=$!

DB=/root/.wingthing/roost.db
seeded=0
for _attempt in $(seq 1 90); do
  if [ -f "$DB" ] && [ "$(sqlite3 "$DB" "SELECT count(*) FROM orgs WHERE slug='slide';" 2>/dev/null)" = "1" ]; then
    sqlite3 "$DB" < /opt/canary/seed.sql
    seeded=1
    break
  fi
  sleep 1
done
if [ "$seeded" = "1" ]; then
  # Seed a provider profile for Alice only. The directory name is
  # userHash("u-alice"); the browser canary verifies that the launched Claude
  # process receives this profile while Bob's distinct identity does not.
  ALICE_HOME=/root/.wingthing/user-homes/e3fb03053ead
  install -d -m 0700 "$ALICE_HOME/.claude"
  printf '%s\n' '{"hasCompletedOnboarding":true,"wtCanaryProfile":"alice-persisted"}' \
    > "$ALICE_HOME/.claude/.claude.json"
  chmod 0600 "$ALICE_HOME/.claude/.claude.json"
  echo CANARY_SEEDED
  touch /tmp/seeded
else
  echo "CANARY_SEED_FAILED: org 'slide' never appeared in $DB" >&2
fi

wait $ROOST_PID
