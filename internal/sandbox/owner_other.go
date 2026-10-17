//go:build !linux && !darwin

package sandbox

import "fmt"

func sandboxBootID() (string, error) { return "", fmt.Errorf("boot identity unavailable") }
func sandboxProcessStart(int) (string, error) {
	return "", fmt.Errorf("process identity unavailable")
}
func validSandboxStart(string) bool { return false }
