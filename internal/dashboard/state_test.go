package dashboard

import (
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

func session(id, name, cwd, status string) eggclient.LocalSession {
	return eggclient.LocalSession{ID: id, Name: name, CWD: cwd, Status: status, Agent: "codex", UptimeSecs: 120}
}

func fixture() *state {
	s := &state{
		machines: map[string]machine{
			"local": {sessions: []eggclient.LocalSession{
				session("one", "build", "/src/app", "working"), session("two", "review", "/src/app", "blocked"),
			}},
			"work": {sessions: []eggclient.LocalSession{session("one", "remote", "/home/me/app", "idle")}},
		},
		defaultAgent: "claude", defaultCWD: "/src/default",
	}
	s.reconcile()
	return s
}

func drive(s *state, input string) ([]action, bool) {
	stream := strings.NewReader(input)
	decoder := &keyDecoder{}
	var actions []action
	buffer := make([]byte, 1)
	for {
		n, err := stream.Read(buffer)
		if err == io.EOF {
			break
		}
		for _, key := range decoder.feed(buffer[:n]) {
			act, quit := s.handle(key)
			if act != nil {
				actions = append(actions, *act)
			}
			if quit {
				return actions, true
			}
		}
	}
	for _, key := range decoder.escape() {
		s.handle(key)
	}
	return actions, false
}

func TestDashboardSortMachineProjectBlockedFirst(t *testing.T) {
	s := &state{machines: map[string]machine{
		"z": {sessions: []eggclient.LocalSession{session("z", "remote", "/a", "blocked")}},
		"a": {sessions: []eggclient.LocalSession{session("a", "remote", "/a", "working")}},
		"local": {sessions: []eggclient.LocalSession{
			session("1", "aaa", "/a", "working"), session("2", "aaa", "/b/./", "idle"),
			session("3", "zzz", "/b", "blocked"), session("4", "bbb", "/a", "blocked"),
			session("5", "first", "/c", "unknown"),
		}},
	}}
	var got []string
	for _, row := range s.rows() {
		got = append(got, row.key())
	}
	want := []string{"local:4", "local:1", "local:3", "local:2", "local:5", "a:a", "z:z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

func TestDashboardFilterAndStableSelection(t *testing.T) {
	s := fixture()
	drive(s, "jj")
	if s.selected != "work:one" {
		t.Fatal(s.selected)
	}
	drive(s, "/CoDeX\r")
	if len(s.rows()) != 3 || s.selected != "work:one" || s.mode != navigate {
		t.Fatalf("case-insensitive filter changed selection: %#v", s)
	}
	drive(s, "/\x15REMOTE")
	if len(s.rows()) != 1 || s.rows()[0].machine != "work" {
		t.Fatal("name filter did not narrow the inventory")
	}
	drive(s, "\x1b")
	if s.filter != "CoDeX" || len(s.rows()) != 3 {
		t.Fatal("Escape did not restore the previous filter")
	}
	drive(s, "/\x15/ src")
	if len(s.rows()) != 0 || s.selected != "" {
		t.Fatal("empty filter result kept an actionable selection")
	}
	drive(s, "\x15BLOCKED\r")
	if s.selected != "local:two" || len(s.rows()) != 1 {
		t.Fatal("status filter did not reconcile selection")
	}
}

func TestDashboardKeysWithFakeInputStream(t *testing.T) {
	for _, test := range []struct {
		name, input, target string
		kind                mode
		attach, quit        bool
		count               int
	}{
		{"arrows", "\x1b[B\x1b[A\x1b[C\x1b[D\r", "local:two", navigate, true, false, 1},
		{"vim", "jjk\r", "local:one", navigate, true, false, 1},
		{"remote", "jj\r", "work:one", navigate, true, false, 1},
		{"rename", "r\x15new-name\r", "local:two", rename, false, false, 1},
		{"stop defaults no", "x\r", "", navigate, false, false, 0},
		{"stop no", "xn", "", navigate, false, false, 0},
		{"stop yes", "xy", "local:two", confirmStop, false, false, 1},
		{"new", "n\x15codex\r\x15/new space\r", "local:two", newCWD, false, false, 1},
		{"quit", "q", "", navigate, false, true, 0},
		{"interrupt prompt", "n\x03", "", navigate, false, true, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := fixture()
			acts, quit := drive(s, test.input)
			if quit != test.quit || len(acts) != test.count {
				t.Fatalf("actions=%v quit=%v", acts, quit)
			}
			if len(acts) > 0 && (acts[0].kind != test.kind || acts[0].row.key() != test.target || acts[0].attach != test.attach) {
				t.Fatalf("wrong action: %#v", acts[0])
			}
			if test.name == "new" && (acts[0].agent != "codex" || acts[0].cwd != "/new space") {
				t.Fatalf("new session lost prompt values: %#v", acts[0])
			}
			if test.name == "rename" && acts[0].name != "new-name" {
				t.Fatal(acts[0].name)
			}
		})
	}
}

func TestDashboardPromptCapturesTargetAndValidates(t *testing.T) {
	s := fixture()
	drive(s, "r\x15")
	s.machines["local"] = machine{sessions: []eggclient.LocalSession{session("replacement", "new", "/src", "idle")}}
	s.reconcile()
	if acts, _ := drive(s, "\r"); len(acts) != 0 || s.mode != rename || s.message == "" {
		t.Fatal("empty name was accepted")
	}
	if acts, _ := drive(s, "bad name\r"); len(acts) != 0 || s.message == "" {
		t.Fatal("invalid name was accepted")
	}
	acts, _ := drive(s, "\x15renamed\r")
	if len(acts) != 1 || acts[0].row.ID != "two" {
		t.Fatal("refresh retargeted the rename")
	}
	s.busy = true
	if acts, _ := drive(s, "\rnxr"); len(acts) != 0 || s.mode != navigate {
		t.Fatal("accepted a second action while busy")
	}
}

func TestDashboardNewSessionWithoutInventory(t *testing.T) {
	s := &state{machines: map[string]machine{}, defaultCWD: "/default"}
	if acts, _ := drive(s, "\rrxy"); len(acts) != 0 {
		t.Fatal("acted on an empty inventory")
	}
	acts, _ := drive(s, "n\r\r")
	if len(acts) != 1 || acts[0].row.machine != "local" || acts[0].cwd != "/default" || acts[0].agent != "" {
		t.Fatalf("new shell = %#v", acts)
	}
}

func TestDashboardDecoderFragmentedUTF8AndEscapes(t *testing.T) {
	d := &keyDecoder{}
	var got []string
	for _, b := range []byte("\x1b[B界\x7f\x1b[23x\x03\x1a") {
		got = append(got, d.feed([]byte{b})...)
	}
	if want := []string{"down", "界", "backspace", "ctrl-c", "ctrl-z"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %q, want %q", got, want)
	}
	if len(d.feed([]byte{0x1b})) != 0 || !reflect.DeepEqual(d.escape(), []string{"escape"}) {
		t.Fatal("standalone Escape was not decoded")
	}
	s := fixture()
	drive(s, "/界\x7f\r")
	if s.filter != "" {
		t.Fatal("backspace split a UTF-8 rune")
	}
}
