package main

import (
	"reflect"
	"testing"
)

func TestAttachInputFilter(t *testing.T) {
	tests := []struct {
		name   string
		chunks [][]byte
		want   []byte
		detach bool
	}{
		{name: "ordinary input", chunks: [][]byte{[]byte("hello")}, want: []byte("hello")},
		{name: "literal prefix", chunks: [][]byte{{attachPrefix, attachPrefix}}, want: []byte{attachPrefix}},
		{name: "unknown chord passes through", chunks: [][]byte{{attachPrefix, 'x'}}, want: []byte{attachPrefix, 'x'}},
		{name: "split literal prefix", chunks: [][]byte{{attachPrefix}, {attachPrefix}}, want: []byte{attachPrefix}},
		{name: "detach lower", chunks: [][]byte{{attachPrefix}, {'q'}}, detach: true},
		{name: "detach upper", chunks: [][]byte{{attachPrefix, 'Q'}}, detach: true},
		{name: "bytes before detach survive", chunks: [][]byte{{'a', attachPrefix, 'q'}}, want: []byte{'a'}, detach: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := &attachInputFilter{}
			var got []byte
			var detached bool
			for _, chunk := range tt.chunks {
				output, detach := filter.filter(chunk)
				got = append(got, output...)
				if detach {
					detached = true
					break
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("output = %v, want %v", got, tt.want)
			}
			if detached != tt.detach {
				t.Fatalf("detach = %v, want %v", detached, tt.detach)
			}
		})
	}
}

func TestValidateSessionID(t *testing.T) {
	for _, valid := range []string{"deadbeef", "session-1", "agent_one", "a.b"} {
		if err := validateSessionID(valid); err != nil {
			t.Errorf("validateSessionID(%q): %v", valid, err)
		}
	}
	for _, invalid := range []string{"", ".", "..", "../egg", "a/b", "two words", "x\ncommand"} {
		if err := validateSessionID(invalid); err == nil {
			t.Errorf("validateSessionID(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestShellQuote(t *testing.T) {
	if got, want := shellQuote("/opt/wing thing/wt"), "'/opt/wing thing/wt'"; got != want {
		t.Fatalf("shellQuote path = %q, want %q", got, want)
	}
	if got, want := shellQuote("it's"), "'it'\"'\"'s'"; got != want {
		t.Fatalf("shellQuote apostrophe = %q, want %q", got, want)
	}
}
