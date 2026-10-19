# Homepage demo

The homepage answers “How do I follow my agents across my machines?” Its
optional, silent video shows the built web app listing eight sessions on two
fictional machines, opening a migration, reading progress, and sending one
instruction to that same session. Native playback controls, English captions,
and an adjacent transcript make the journey available without automatic playback.

Regenerate from the repository root with Node, Playwright's Chromium, Go, and
FFmpeg with the VP9 encoder (set `FFMPEG` if it is not on `PATH`):

```sh
nice -n 10 make web
nice -n 10 node web/test/homepage-demo.mjs --record
nice -n 10 make web
```

The recording updates `web/public/demo/homepage.webm`, `poster.png`, and
`captions.vtt`. App bootstrap screens are trimmed; the interaction is continuous.
The second build embeds them in the binary's web assets. Commit
all three together. Without `--record`, the script verifies the journey without
changing the published media. Set `WT_DEMO_OUT` to retain screenshots and the
receipt elsewhere; the default is `.scratch/homepage-demo`.

`web/test/support/homepage-demo-fixture.js` supplies fictional discovery and
encrypted wing replies in memory. Every browser request must match the local
fixture origin; unexpected requests and duplicate or misdirected prompts fail.
The shipped app's modules and styles render the screens and perform the send.
No provider, account service, running daemon, or personal home is used. These
are illustrative replies, not evidence of a real database migration.

The script also checks 390 and 1440 pixel layouts with zero, one, and eight
long-named sessions. This session inventory has no subscription plan item cap;
eight is the recording's fixture volume, not a product limit. The homepage
keeps its compact two-session preview above the optional player.

Inspect the poster and sample frames, and OCR the whole recording, before
committing regenerated media. Check that no private names, paths, addresses,
accounts, or unrelated screen content appear. The video must remain silent,
user initiated, and playable directly from the embedded `/app/demo/` assets.
