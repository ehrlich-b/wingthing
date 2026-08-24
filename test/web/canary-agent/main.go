// canary-agent is the browser E2E suite's stand-in for an agent CLI. The
// sealed shared-host runtime only projects self-contained native binaries, so
// unlike the Linux battery's probe-oriented mock-agent this one stays
// interactive: it prints a banner and echoes each input line, giving the
// Playwright driver a live session for input, reattach, and kill flows.
package main

import (
	"fmt"
	"os"
	"slices"
)

func main() {
	if slices.Contains(os.Args[1:], "--version") {
		fmt.Println("canary-agent v1")
		return
	}
	host, _ := os.Hostname()
	wd, _ := os.Getwd()
	fmt.Printf("CANARY_SHELL_READY host=%s cwd=%s\r\n> ", host, wd)

	buf := make([]byte, 1024)
	line := make([]byte, 0, 256)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}
		for _, b := range buf[:n] {
			switch b {
			case '\r', '\n':
				fmt.Printf("\r\nECHO:%s\r\n> ", line)
				line = line[:0]
			case 0x03, 0x04: // ^C / ^D end the session
				return
			default:
				line = append(line, b)
			}
		}
	}
}
