package sandbox

import (
	"bytes"
	"io"
	"log"
	"testing"
)

func TestSandboxDiagnosticsAreSeparateFromWarnings(t *testing.T) {
	var ordinary, verbose bytes.Buffer
	prior := log.Writer()
	log.SetOutput(&ordinary)
	defer log.SetOutput(prior)
	logDiagnostic(Config{Diagnostics: io.Discard}, "implementation detail %s", "test")
	if ordinary.Len() != 0 {
		t.Fatal("diagnostic escaped quiet mode")
	}
	logDiagnostic(Config{Diagnostics: &verbose}, "implementation detail %s", "test")
	if verbose.String() != "implementation detail test\n" {
		t.Fatal(verbose.String())
	}
}
