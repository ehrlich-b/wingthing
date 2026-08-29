#!/bin/sh
# Browser E2E tier: shared-roost (org mode) canary in Docker.
# Run via `make test-web` — it cross-compiles wt and mock-agent for the
# docker host arch into this directory first. Plain docker, no compose,
# works on rootless docker.
set -e
cd "$(dirname "$0")"

if [ ! -x ./wt ] || [ ! -x ./claude ]; then
  echo "missing ./wt or ./claude — run: make test-web" >&2
  exit 2
fi

mkdir -p out
rm -f out/*.png out/results.json out/legacy-org-results.json out/direct-results.json out/roost.log out/legacy-roost.log out/direct.log 2>/dev/null || true

docker rm -f wt-web-roost wt-web-roost-legacy wt-web-hosted wt-web-e2e >/dev/null 2>&1 || true
docker network rm wt-web-net >/dev/null 2>&1 || true

echo "== building roost image =="
docker build -q -f Dockerfile.roost -t wt-web-roost .
echo "== building e2e image =="
docker build -q -f Dockerfile.e2e -t wt-web-e2e .

docker network create wt-web-net
# --privileged: shared-roost browser terminals run inside the sealed Linux
# jail, which needs user-namespace creation inside the container. Without it
# eggs fail closed with "system blocked sandbox namespace creation".
docker run -d --name wt-web-roost --network wt-web-net --network-alias roost \
  --privileged \
  -e WT_BASE_URL=http://roost:8080 \
  -e GOOGLE_CLIENT_ID=canary-dummy-client \
  -e GOOGLE_CLIENT_SECRET=canary-dummy-secret \
  -e WT_ROOST_ALLOWED_EMAILS=alice@slide.tech,bob@slide.tech,carol@slide.tech \
  -e WT_JWT_SECRET=canary-jwt-secret-not-production \
  wt-web-roost

echo "== waiting for roost seed =="
seeded=0
i=0
while [ $i -lt 120 ]; do
  if docker exec wt-web-roost test -f /tmp/seeded 2>/dev/null; then seeded=1; break; fi
  if ! docker ps -q --filter name=wt-web-roost | grep -q .; then break; fi
  sleep 2
  i=$((i+1))
done
if [ "$seeded" != "1" ]; then
  echo "ROOST_NEVER_SEEDED" >&2
  docker logs wt-web-roost > out/roost.log 2>&1 || true
  docker rm -f wt-web-roost >/dev/null 2>&1 || true
  docker network rm wt-web-net >/dev/null 2>&1 || true
  exit 1
fi
echo "== roost seeded, running e2e =="

set +e
docker run --rm --name wt-web-e2e --network wt-web-net \
  -e ROOST_URL=http://roost:8080 -e OUT_DIR=/out \
  -v "$(pwd)/out:/out" \
  wt-web-e2e
E2E_RC=$?
set -e

docker logs wt-web-roost > out/roost.log 2>&1 || true
docker rm -f wt-web-roost >/dev/null 2>&1 || true

echo "== starting legacy org-mode compatibility canary =="
docker run -d --name wt-web-roost-legacy --network wt-web-net --network-alias roost \
  --privileged \
  -e WT_BASE_URL=http://roost:8080 \
  -e GOOGLE_CLIENT_ID=canary-dummy-client \
  -e GOOGLE_CLIENT_SECRET=canary-dummy-secret \
  -e WT_JWT_SECRET=canary-jwt-secret-not-production \
  wt-web-roost

seeded=0
i=0
while [ $i -lt 120 ]; do
  if docker exec wt-web-roost-legacy test -f /tmp/seeded 2>/dev/null; then seeded=1; break; fi
  if ! docker ps -q --filter name=wt-web-roost-legacy | grep -q .; then break; fi
  sleep 2
  i=$((i+1))
done
if [ "$seeded" = "1" ]; then
  set +e
  docker run --rm --name wt-web-e2e --network wt-web-net \
    -e ROOST_URL=http://roost:8080 -e OUT_DIR=/out \
    -v "$(pwd)/out:/out" \
    wt-web-e2e node /e2e/legacy-org.mjs
  LEGACY_ORG_RC=$?
  set -e
else
  echo "LEGACY_ORG_CANARY_NEVER_SEEDED" >&2
  LEGACY_ORG_RC=1
fi

docker logs wt-web-roost-legacy > out/legacy-roost.log 2>&1 || true
docker rm -f wt-web-roost-legacy >/dev/null 2>&1 || true

echo "== starting hosted direct-only policy canary =="
docker run -d --name wt-web-hosted --network wt-web-net --network-alias hosted \
  -e WT_BASE_URL=http://hosted:8080 \
  -e GOOGLE_CLIENT_ID=canary-dummy-client \
  -e GOOGLE_CLIENT_SECRET=canary-dummy-secret \
  -e WT_JWT_SECRET=canary-jwt-secret-not-production \
  -e WT_RELAY_POLICY=direct-free \
  -e WT_RELAY_MIGRATION_BEFORE=2026-08-26T00:00:00Z \
  wt-web-roost /opt/canary/entry-direct.sh

seeded=0
i=0
while [ $i -lt 120 ]; do
  if docker exec wt-web-hosted test -f /tmp/seeded 2>/dev/null; then seeded=1; break; fi
  if ! docker ps -q --filter name=wt-web-hosted | grep -q .; then break; fi
  sleep 2
  i=$((i+1))
done
if [ "$seeded" = "1" ]; then
  set +e
  docker run --rm --name wt-web-e2e --network wt-web-net \
    -e ROOST_URL=http://hosted:8080 -e OUT_DIR=/out \
    -v "$(pwd)/out:/out" \
    wt-web-e2e node /e2e/direct-only.mjs
  DIRECT_RC=$?
  set -e
else
  echo "HOSTED_DIRECT_CANARY_NEVER_SEEDED" >&2
  DIRECT_RC=1
fi

docker logs wt-web-hosted > out/direct.log 2>&1 || true
docker rm -f wt-web-hosted >/dev/null 2>&1 || true
docker network rm wt-web-net >/dev/null 2>&1 || true
echo "web e2e exit codes: org=$E2E_RC legacy-org=$LEGACY_ORG_RC direct=$DIRECT_RC (artifacts in test/web/out/)"
if [ "$E2E_RC" -ne 0 ] || [ "$LEGACY_ORG_RC" -ne 0 ] || [ "$DIRECT_RC" -ne 0 ]; then exit 1; fi
