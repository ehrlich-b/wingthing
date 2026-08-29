#!/bin/sh
set -e

/usr/local/bin/wt serve --addr :8080 &
SERVER_PID=$!

DB=/root/.wingthing/roost.db
seeded=0
for _attempt in $(seq 1 90); do
  if [ -f "$DB" ] && [ "$(sqlite3 "$DB" "SELECT count(*) FROM schema_migrations;" 2>/dev/null)" -gt 0 ] 2>/dev/null; then
    sqlite3 "$DB" < /opt/canary/seed-direct.sql
    seeded=1
    break
  fi
  sleep 1
done
if [ "$seeded" = "1" ]; then
  echo DIRECT_CANARY_SEEDED
  touch /tmp/seeded
else
  echo "DIRECT_CANARY_SEED_FAILED" >&2
fi

wait $SERVER_PID
