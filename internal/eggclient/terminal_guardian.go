package eggclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/muesli/cancelreader"
	"golang.org/x/term"
)

// A separate process owns the pre-attach terminal state. Pipe EOF survives even
// SIGKILL of the client; the guardian neither holds nor controls the egg stream.
// Re-exec avoids forking a multithreaded Go runtime without exec.
const terminalGuardianArg = "--wt-terminal-guardian"
const terminalCommandArg = "--wt-terminal-command"

type terminalSnapshot struct {
	Pending []byte
	Known   map[int]int
}

var outerModes = []int{1, 9, 25, 47, 1047, 1049, 1000, 1001, 1002, 1003, 1004, 1005, 1006, 1007, 1015, 1016, 2004, 2026}
var modeReport = regexp.MustCompile("\x1b\\[\\?([0-9]+);([0-4])\\$y")
var cursorReport = regexp.MustCompile("\x1b\\[[0-9]+;[0-9]+R")

func init() {
	if len(os.Args) == 2 && (os.Args[1] == terminalGuardianArg || os.Args[1] == terminalCommandArg) {
		code, err := runTerminalGuardian()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(code)
	}
}

func modeList() string {
	parts := make([]string, len(outerModes))
	for i, mode := range outerModes {
		parts[i] = strconv.Itoa(mode)
	}
	return strings.Join(parts, ";")
}

func runTerminalGuardian() (code int, err error) {
	life := os.NewFile(3, "client-lifetime")
	input := os.NewFile(4, "terminal-input")
	output := os.NewFile(5, "terminal-output")
	ready := os.NewFile(6, "guardian-ready")
	defer life.Close()
	defer input.Close()
	defer output.Close()
	defer ready.Close()
	signal.Ignore(syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	state, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, term.Restore(int(input.Fd()), state)) }()
	// XTSAVE is the fallback for terminals without DECRQM. Provider saves are
	// virtualized by terminalModeWriter so these private slots remain protected.
	if _, err := fmt.Fprintf(output, "\x1b[?%ss", modeList()); err != nil {
		return 0, err
	}
	restore := []byte("\x18\x1b[?" + modeList() + "r")
	defer func() { _, writeErr := output.Write(restore); err = errors.Join(err, writeErr) }()
	reader, err := cancelreader.NewReader(input)
	if err != nil {
		return 0, err
	}
	// Old terminals may not implement queries. This bounds negotiation only;
	// the client lifetime below is driven exclusively by pipe EOF.
	timer := time.AfterFunc(250*time.Millisecond, func() { reader.Cancel() })
	var query bytes.Buffer
	for _, mode := range outerModes {
		fmt.Fprintf(&query, "\x1b[?%d$p", mode)
	}
	query.WriteString("\x1b[6n") // ordered reply fence
	if _, err := output.Write(query.Bytes()); err != nil {
		timer.Stop()
		reader.Close()
		return 0, err
	}
	var replies []byte
	buf := make([]byte, 4096)
	for !cursorReport.Match(replies) {
		n, readErr := reader.Read(buf)
		replies = append(replies, buf[:n]...)
		if readErr != nil {
			break
		}
	}
	timer.Stop()
	if err := reader.Close(); err != nil {
		return 0, err
	}
	known := map[int]int{}
	for _, match := range modeReport.FindAllSubmatch(replies, -1) {
		mode, _ := strconv.Atoi(string(match[1]))
		value, _ := strconv.Atoi(string(match[2]))
		if value != 0 {
			known[mode] = value
		}
	}
	// Restore unknown modes from XTSAVE, then known values explicitly. Restore
	// resets before sets so alternate-screen aliases cannot override one another.
	restore = []byte{0x18}
	for _, mode := range outerModes {
		if _, ok := known[mode]; !ok {
			restore = fmt.Appendf(restore, "\x1b[?%dr", mode)
		}
	}
	for _, enabled := range []bool{false, true} {
		for _, mode := range outerModes {
			value, ok := known[mode]
			if !ok || (value == 1 || value == 3) != enabled {
				continue
			}
			final := 'l'
			if enabled {
				final = 'h'
			}
			restore = fmt.Appendf(restore, "\x1b[?%d%c", mode, final)
		}
	}
	// Preserve keys typed during negotiation; never send query replies to the egg.
	pending := cursorReport.ReplaceAll(modeReport.ReplaceAll(replies, nil), nil)
	if err := json.NewEncoder(ready).Encode(terminalSnapshot{Pending: pending, Known: known}); err != nil {
		return 0, err
	}
	ready.Close()
	if os.Args[1] == terminalCommandArg {
		return runGuardedTerminalCommand(life, input, output, pending, known)
	}
	_, err = io.Copy(io.Discard, life)
	return 0, err
}

func guardTerminal(input, output *os.File) (func() error, terminalSnapshot, error) {
	lifeRead, lifeWrite, err := os.Pipe()
	if err != nil {
		return nil, terminalSnapshot{}, err
	}
	defer lifeRead.Close()
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		lifeWrite.Close()
		return nil, terminalSnapshot{}, err
	}
	defer readyRead.Close()
	defer readyWrite.Close()
	exe, err := os.Executable()
	if err != nil {
		lifeWrite.Close()
		return nil, terminalSnapshot{}, err
	}
	cmd := exec.Command(exe, terminalGuardianArg)
	cmd.ExtraFiles = []*os.File{lifeRead, input, output, readyWrite}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		lifeWrite.Close()
		return nil, terminalSnapshot{}, err
	}
	readyWrite.Close()
	finish := func() error { lifeWrite.Close(); return cmd.Wait() }
	var snapshot terminalSnapshot
	if err := json.NewDecoder(readyRead).Decode(&snapshot); err != nil {
		_ = finish()
		return nil, terminalSnapshot{}, fmt.Errorf("snapshot terminal modes: %w", err)
	}
	return finish, snapshot, nil
}
