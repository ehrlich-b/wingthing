# Deploy

## Fly.io Setup (one-time)

```bash
flyctl apps create wingthing
flyctl volumes create wt_data --region ewr --size 1
flyctl secrets set OPENAI_API_KEY=...
```

## Deploy

```bash
flyctl deploy
```

## Seed DB from local backup

```bash
cp ~/.wingthing/social.db ~/wt_bak/social_$(date +%Y%m%d_%H%M).db
flyctl ssh sftp shell
> put ~/wt_bak/social_LATEST.db /data/.wingthing/social.db
flyctl machines restart
```

## Re-seed after local pipeline run

```bash
cp ~/.wingthing/social.db ~/wt_bak/social_$(date +%Y%m%d_%H%M).db
flyctl ssh sftp shell
> put ~/.wingthing/social.db /data/.wingthing/social.db
flyctl machines restart
```

## DNS

Point `wt.ai` to fly: `flyctl certs create wt.ai`
