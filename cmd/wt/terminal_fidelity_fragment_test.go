package main

import (
	"bytes"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

// Exercise a large bracketed paste across arbitrary transport read boundaries.
func TestTerminalFidelityPasteFilter(t *testing.T) {
	payload := bytes.Repeat([]byte("wide-界\x02q"), 131072)
	payload = payload[:1<<20]
	input := append([]byte("\x1b[200~"), payload...)
	input = append(input, []byte("\x1b[201~")...)
	var filter eggclient.AttachInputFilter
	var got []byte
	for offset := 0; offset < len(input); {
		end := min(offset+4093, len(input))
		part, detached := filter.Filter(input[offset:end])
		if detached {
			t.Fatal("paste triggered detach")
		}
		got = append(got, part...)
		offset = end
	}
	got = append(got, filter.Flush()...)
	if !bytes.Equal(got, input) {
		t.Fatal("paste bytes changed")
	}
}

func TestTerminalFidelityFocusReset(t *testing.T) {
	if !bytes.Contains(egg.NativeTerminalReset(), []byte("\x1b[?1004l")) {
		t.Fatal("focus reporting left enabled by cleanup")
	}
}
