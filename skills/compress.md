---
name: compress
description: Fetch RSS feeds and compress articles to 1024 chars
memory:
  - feeds
tags: [rss, compress, content]
---
# Compress

Read the RSS feed URLs listed below. For each feed, extract the 5 most recent articles.

For each article, compress the full text down to a maximum of 1024 characters. Preserve the key facts, quotes, and conclusions. Drop filler, attribution chains, and redundant context.

## Feeds

{{memory.feeds}}

## Output Format

For each article, output exactly this structure:

```
### [title]
**Source:** [feed name] | **Date:** [publication date]

[compressed text, max 1024 chars]
```

Order by publication date, most recent first. If a feed URL is unreachable, skip it and note the error at the end.
