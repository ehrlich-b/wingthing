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

var outerModes = []int{1, 9, 25, 47, 1047, 1049, 1000, 1001, 1002, 1003, 1004, 1005, 1006, 1007, 1015, 1016, 2004, 2026}
var modeReport = regexp.MustCompile("\x1b\\[\\?([0-9]+);([0-4])\\$y")
var cursorReport = regexp.MustCompile("\x1b\\[[0-9]+;[0-9]+R")

func init() {
	if len(os.Args) == 2 && os.Args[1] == terminalGuardianArg {
		if err := runTerminalGuardian(); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
}

func modeList() string {
	parts := make([]string, len(outerModes))
	for i, mode := range outerModes {
		parts[i] = strconv.Itoa(mode)
	}
	return strings.Join(parts, ";")
}

func runTerminalGuardian() (err error) {
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
		return err
	}
	defer func() { err = errors.Join(err, term.Restore(int(input.Fd()), state)) }()
	// XTSAVE is the fallback for terminals without DECRQM. Explicit reports are
	// preferred: a provider's own XTSAVE must not overwrite our known snapshot.
	if _, err := fmt.Fprintf(output, "\x1b[?%ss", modeList()); err != nil {
		return err
	}
	restore := []byte("\x18\x1b[?" + modeList() + "r")
	defer func() { _, writeErr := output.Write(restore); err = errors.Join(err, writeErr) }()
	reader, err := cancelreader.NewReader(input)
	if err != nil {
		return err
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
		return err
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
		return err
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
	if err := json.NewEncoder(ready).Encode(pending); err != nil {
		return err
	}
	ready.Close()
	_, err = io.Copy(io.Discard, life)
	return err
}

func guardTerminal(input, output *os.File) (func() error, []byte, error) {
	lifeRead, lifeWrite, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	defer lifeRead.Close()
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		lifeWrite.Close()
		return nil, nil, err
	}
	defer readyRead.Close()
	defer readyWrite.Close()
	exe, err := os.Executable()
	if err != nil {
		lifeWrite.Close()
		return nil, nil, err
	}
	cmd := exec.Command(exe, terminalGuardianArg)
	cmd.ExtraFiles = []*os.File{lifeRead, input, output, readyWrite}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		lifeWrite.Close()
		return nil, nil, err
	}
	readyWrite.Close()
	finish := func() error { lifeWrite.Close(); return cmd.Wait() }
	var pending []byte
	if err := json.NewDecoder(readyRead).Decode(&pending); err != nil {
		_ = finish()
		return nil, nil, fmt.Errorf("snapshot terminal modes: %w", err)
	}
	return finish, pending, nil
}
