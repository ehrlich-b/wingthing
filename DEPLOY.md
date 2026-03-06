# Deploy

## Fly.io Setup (one-time)

```bash
flyctl apps create wingthing
flyctl volumes create wt_data --region ewr --size 1
flyctl secrets set WT_JWT_KEY=$(wt keygen)
```

## Deploy

```bash
flyctl deploy
```

## DNS

Point `wt.ai` to fly: `flyctl certs create wt.ai`
