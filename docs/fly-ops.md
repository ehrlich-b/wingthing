# Fly Operations Guide

## Architecture

Two process groups, one image:

- **login** — has the SQLite volume at `/data`. Handles auth, API, social pages, WebSocket relay. There is exactly one.
- **edge** — stateless. Handles WebSocket relay only. Proxies API/auth to login. There can be zero or many.

Role is auto-detected: if `/data` exists (volume mounted), it's login. Otherwise edge. No env vars to set per machine.

Edge nodes discover the login node via Fly internal DNS: `login.process.wingthing.internal:8080`.

## One-time setup

Generate an EC P-256 signing key so wings can auth against any node:

```
fly secrets set WT_JWT_KEY=$(wt keygen)
```

## Deploy

Build and push to all machines:

```
make deploy
```

This runs `make check` first (tests + build), then `fly deploy`.

## Scale

### Add edge nodes to a region

```
make deploy-edge REGIONS=nrt COUNT=1      # 1 edge in Tokyo
make deploy-edge REGIONS=lhr COUNT=1      # 1 edge in London
make deploy-edge REGIONS=nrt,lhr COUNT=2  # 2 edges split across Tokyo + London
```

### Set total counts

```
make scale LOGIN=1 EDGE=3
```

### Check what's running

```
make status
```

## Middle-of-the-night playbook

```
make deploy
make deploy-edge REGIONS=nrt,lhr,cdg COUNT=1
```

That's it. One login in ewr, one edge each in Tokyo, London, Paris. Wings and browsers auto-route to nearest via Fly anycast.

## Region codes

| Code | City |
|------|------|
| ewr | Newark (login node) |
| nrt | Tokyo |
| lhr | London |
| cdg | Paris |
| sin | Singapore |
| syd | Sydney |
| gru | São Paulo |
| sea | Seattle |
| ord | Chicago |
| iad | Ashburn |

Full list: `fly platform regions`

## How it works

1. `fly deploy` builds the Docker image and deploys to all machines
2. Login machine has the `wt_data` volume → auto-detects as login
3. Edge machines have no volume → auto-detect as edge
4. Edges proxy API/auth requests to login over Fly's private 6PN network
5. Edges cache entitlements (polled every 60s) and sessions (cached 5min)
6. Login drives gossip: pushes wing online/offline events to edges every 2s
7. If a browser on an edge needs a wing on another node, `fly-replay` header redirects the WebSocket upgrade transparently

## Removing edge nodes

```
fly scale count edge=0 --region nrt    # remove Tokyo edges
```

Or remove all edges:

```
make scale LOGIN=1 EDGE=0
```

## Self-hosted

Self-hosted is single node. No `WT_NODE_ROLE`, no `FLY_MACHINE_ID`, no gossip, no fly-replay. Just `wt serve`. All multi-node code paths are gated on Fly env vars being present.
