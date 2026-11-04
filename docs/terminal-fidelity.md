# Terminal fidelity: current evidence and gaps

The attachment's native terminal mode forwards provider output, but a full
sequence-by-sequence comparison with a direct PTY has not yet been performed.
No conclusion about parity is available for DA1, DA2, DSR/CPR, OSC 10/11,
OSC 52, OSC 8, OSC 0/2, synchronized output, kitty keyboard, truecolor,
cursor shape, alternate screen, resize, UTF-8 or input through a real attach.

The paste filter fragment test sends a 1 MiB bracketed payload, including
Ctrl+B q, in uneven chunks and checks byte equality. This is not an end-to-end
PTY test. The existing native mouse PTY test covers SGR wheel delivery; mouse
handling intentionally differs for browser projections.

Cleanup now disables focus reporting along with the other tracked modes. The
reset still assumes default outer terminal modes rather than recording their
previous values. An enabled mode before attach can therefore be disabled on
return. An uncatchable kill cannot execute the client's deferred cleanup; a
separate surviving owner would be needed for that case. Neither restoration
case has been established by a terminal emulator state comparison.
