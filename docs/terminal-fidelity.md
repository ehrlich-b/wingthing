# Terminal fidelity

`TestTerminalFidelityPTY` runs the same recording Python provider on a plain
kernel PTY, then on an egg PTY through the built `wt attach` CLI. Both runs use
the same deterministic protocol terminal endpoint. The egg's VT shadow is
active (`--vte`); its replies must never substitute for the outer terminal's
replies. Each output segment is compared byte for byte, and the provider checks
its input against the fixture before recording its length, SHA-256 and (for
small inputs) hex. The two provider recordings must be identical.

This is a protocol/pass-through measurement, not a claim about every emulator's
rendering, clipboard permissions or hyperlink UI. The endpoint supplies fixed
DA/colour/clipboard/keyboard replies and implements DEC mode reporting. It does
not use the egg's VT implementation. The provider needs only Python and a PTY;
the fixture uses the existing trusted outer-boundary option with a minimal
local environment and no provider account or network requests. Sandbox policy
is covered separately by the existing native mouse journey.

## Measured results

The focused test passes all 28 rows below. “Exact” means the provider output
arrives unchanged; input/reply sizes are the bytes recorded by the provider.
The reproducible case definitions are in
`cmd/wt/testdata/terminal_fidelity_cases.json`, with the recording program in
`terminal_fidelity.py` alongside them.

| Behaviour | Direct PTY result | Attached PTY result |
| --- | --- | --- |
| DA1 | Exact; 13-byte terminal reply | Same output and reply |
| DA2 | Exact; 12-byte terminal reply | Same output and reply |
| DSR status | Exact; `CSI 0 n` | Same output and reply |
| CPR after positioning | Exact; `CSI 7;13 R` | Same output and reply |
| OSC 10 foreground query | Exact; 25-byte RGB reply | Same output and reply |
| OSC 11 background query | Exact; 25-byte RGB reply | Same output and reply |
| OSC 52 clipboard set | Exact OSC bytes | Same bytes |
| OSC 52 clipboard query | Exact; 21-byte reply | Same output and reply |
| OSC 8 hyperlink | Exact open/text/close | Same bytes |
| OSC 0 title | Exact, including wide title | Same bytes |
| OSC 2 title | Exact | Same bytes |
| Bracketed paste | Exact enable; 20-byte paste | Same output and input |
| Focus in, mode 1004 | Exact enable; `CSI I` input | Same output and input; browser enable also preserved |
| Focus out | `CSI O` input | Same input |
| Synchronized output, 2026 | Exact begin/text/end | Same bytes |
| SGR mouse | Exact enables; 18-byte press/release | Same output and input |
| Wheel up | `CSI <64;5;3 M`, 10 bytes | Same input |
| Wheel down | `CSI <65;5;3 M`, 10 bytes | Same input |
| Kitty keyboard query | Exact; `CSI ?5 u` reply | Same output and reply |
| Kitty keyboard push | Exact push; 7-byte encoded key | Same output and input |
| Kitty keyboard pop | Exact pop | Same bytes |
| Truecolor SGR | Exact foreground/background/reset | Same bytes |
| Alternate screen | Exact enter/text/leave | Same bytes |
| DECSCUSR | Exact `CSI 5 SP q` | Same bytes |
| SIGWINCH | Provider reports 93 columns × 31 rows | Same dimensions via CLI resize forwarding |
| Ctrl+B | Provider reads one `02` byte | Same byte after documented Ctrl+B twice escape |
| UTF-8 wide characters | Exact `界🙂é` bytes | Same bytes |
| 1 MiB bracketed paste | 1,048,588 bytes including markers | Same length and SHA-256; no loss or reorder |

The large paste includes UTF-8 and literal Ctrl+B q sequences. Its complete
marker-plus-payload SHA-256 in both recordings is
`541f944788d1fae87af4dc68c1313b03d63c79d6edc09460aaa6277165972eeb`.
The earlier uneven-chunk paste filter regression remains in the focused suite.

## Differences and cleanup

There are zero unexplained differences in the measured provider sequences.
Browser presentation intentionally replaces mouse-enable requests with the
existing tracking marker, leaving terminal selection available. Native attach
preserves those requests. The read-only browser observer asserts that mouse
requests are projected while focus mode 1004 is forwarded; both SGR wheel
sequences above still reach the provider, matching the wheel contract in
`northstar_native_mouse_pty_test.go`.

Outside provider segments, attach adds its diagnostic, detach key handling and
terminal-state negotiation. Ctrl+B twice sends one literal Ctrl+B; Ctrl+B q
locally detaches. Bracketed paste bypasses that key handling. These are existing
attachment controls, not provider byte loss.

A separate terminal guardian snapshots modes before attachment using DECRQM,
with a CPR reply as an ordered negotiation fence. Explicit reported values are
restored rather than assuming default modes. The guardian owns a lifetime pipe
and the original termios state; EOF from normal detach or client SIGKILL triggers
restoration. It has its own process session so a client process-group signal
does not remove the cleanup owner. It never holds the provider connection.

The cleanup subtest covers normal detach and SIGKILL for three initial states:
defaults, enabled mouse/paste/focus/alternate screen with a hidden cursor, and a
mixed mouse/focus/alternate-screen state with a visible cursor. Each state runs
with DECRQM reporting, unsupported-mode reports, and no DECRQM replies (18 local
cases). It checks all tracked mouse modes, paste, alternate selectors, visibility,
focus, application cursor keys, synchronized output and termios. The provider
saves and restores both its initial and modified modes before cleanup, so the
test verifies provider save semantics as well as protection of the outer state.
PTY EOF fences the guardian's final output and termios restoration; tests use
events, not sleeps or timing thresholds.

`TestTerminalFidelityEmptyParameters` repeats those 18 local cleanup cases with
leading, interior and trailing omitted CSI parameters in provider mode changes,
saves and restores. Omitted parameters mean mode zero; they must not bypass
save-slot protection for other modes in the same sequence. In particular,
`CSI ?25;1000;1004;2004;s` cannot overwrite
the outer snapshot. Both detach and SIGKILL restore the original cursor, mouse,
focus and paste states even without DECRQM replies. Provider saves and restores
still reproduce the provider's current modes before cleanup.

`TestTerminalFidelityIgnoredDEL` adds 18 local cleanup cases with DEL at CSI
entry, inside mode numbers, around separators and before final bytes. The
independent terminal endpoint ignores DEL while retaining the original wire
transcript. Provider changes, saves and restores preserve their mode semantics;
in particular, `CSI ?25;1000;1004;2004 DEL s` cannot overwrite the outer snapshot.
Detach and SIGKILL restore all tracked modes and termios for all three initial
states and reporting capabilities, including no DECRQM replies. The attachment
writer removes ignored DEL before interpreting parameters, while ordinary
pass-through sequences retain their original bytes.

Terminals without DECRQM use protected XTSAVE/XTRESTORE slots for unsupported
modes. Provider saves are virtualized separately in the attachment's mode writer;
provider restores become explicit mode changes or references to the protected
original. This preserves mode semantics while reserving the terminal's slots
for cleanup, including when the provider saves before its first mode change.
Only mode save/restore encodings are translated; the 28 measured provider
sequences remain byte exact. A provider cannot overwrite the fallback snapshot.
Negotiation has a bounded 250 ms compatibility fallback if CPR is unavailable.
An emulator supporting neither reporting nor saved modes cannot expose its
prior private-mode state; exact restoration on such an emulator is unverified.

Both SSH routes (`--remote HOST attach SESSION` and `attach NAME:SESSION`) use
the same snapshot and save-slot isolation on the local outer terminal. A guardian
supervises SSH on a private PTY, forwards negotiation typeahead and window changes,
and stops SSH's process group before restoration when the local client dies.
The SSH cleanup test uses a local fake SSH executable through the real CLI
dispatch: both routes, all three initial states, all three reporting capabilities,
and normal exit or client SIGKILL (36 cases). It checks saved-mode semantics,
resize delivery, typeahead, final modes and termios without a remote connection.
Likewise, destruction of the terminal itself or killing the guardian cannot be
repaired by a surviving client. These limits do not change measured parity on
the reporting endpoint.

## Reproduce

Use a short checkout so the real egg Unix socket fits, then run the registered
web prerequisite and the focused package tests:

```sh
nice -n 10 make web
WT_FIDELITY_EVIDENCE="$(mktemp -d)" nice -n 10 go test -p 2 -count=1 -run '^TestTerminalFidelity' ./cmd/wt -v
```

The evidence directory receives direct/attached JSON provider recordings,
direct/attached/browser ANSI transcripts and 90 local/SSH cleanup transcripts
(18 under `empty-parameters/` and 18 under `ignored-del/`). Without that optional
environment variable, records stay in the test's temporary directory. No real provider, browser download, root or external service is used.
