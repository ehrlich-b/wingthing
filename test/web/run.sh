#!/bin/sh
# Browser E2E tier: shared-roost (org mode) canary in Docker.
# Run via `make test-web` — it cross-compiles wt and mock-agent for the
# docker host arch into this directory first. Plain docker, no compose,
# works on rootless docker.
set -e
cd "$(dirname "$0")"

if [ ! -x ./wt ] || [ ! -x ./mock-agent ]; then
  echo "missing ./wt or ./mock-agent — run: make test-web" >&2
  exit 2
fi

mkdir -p out
rm -f out/*.png out/results.json out/roost.log 2>/dev/null || true

docker rm -f wt-web-roost wt-web-e2e >/dev/null 2>&1 || true
docker network rm wt-web-net >/dev/null 2>&1 || true

echo "== building roost image =="
docker build -q -f Dockerfile.roost -t wt-web-roost .
echo "== building e2e image =="
docker build -q -f Dockerfile.e2e -t wt-web-e2e .

docker network create wt-web-net
docker run -d --name wt-web-roost --network wt-web-net --network-alias roost \
  -e WT_BASE_URL=http://roost:8080 \
  -e GOOGLE_CLIENT_ID=canary-dummy-client \
  -e GOOGLE_CLIENT_SECRET=canary-dummy-secret \
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
docker network rm wt-web-net >/dev/null 2>&1 || true
echo "web e2e exit code: $E2E_RC (artifacts in test/web/out/)"
exit $E2E_RC
