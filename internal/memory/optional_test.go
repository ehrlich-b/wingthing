package memory

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func TestAbsentOptionalMemoryDoesNotWarn(t *testing.T) {
	var out bytes.Buffer
	prior := log.Writer()
	log.SetOutput(&out)
	defer log.SetOutput(prior)
	s := New(filepath.Join(testsocket.Dir(t), "not-created"))
	if got := s.Load("index"); got != "" {
		t.Fatal(got)
	}
	s.LoadAll()
	if out.Len() != 0 {
		t.Fatalf("optional memory warned: %s", out.String())
	}
}
func TestUnreadableOptionalMemoryStillWarns(t *testing.T) {
	var out bytes.Buffer
	prior := log.Writer()
	log.SetOutput(&out)
	defer log.SetOutput(prior)
	dir := testsocket.Dir(t)
	if err := os.Mkdir(filepath.Join(dir, "index.md"), 0700); err != nil {
		t.Fatal(err)
	}
	New(dir).Load("index")
	if out.Len() == 0 {
		t.Fatal("real read failure hidden")
	}
}
