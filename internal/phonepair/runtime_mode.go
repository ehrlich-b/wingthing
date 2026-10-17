package phonepair

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	ModeLocalOnly = "local-only"
	ModeRelay     = "relay"
)

type RuntimeMode struct {
	WingID string `json:"wing_id"`
	Mode   string `json:"mode"`
}

// RunningMode asks the running owner of the private control token for its mode.
// Old servers, dead daemons and incomplete replies all produce an unknown mode.
// Saved arguments, status files and invitation URLs cannot establish live mode.
func RunningMode(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "phone-control.json"))
	if err != nil {
		return ""
	}
	var control Control
	if json.Unmarshal(data, &control) != nil || control.Port < 1 || control.Port > 65535 || control.Token == "" || control.WingID == "" {
		return ""
	}
	req, err := http.NewRequest("POST", control.AdminURL(), strings.NewReader(`{"Op":"mode"}`))
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+control.Token)
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ""
	}
	data, err = io.ReadAll(io.LimitReader(response.Body, 4097))
	var mode RuntimeMode
	if err != nil || len(data) > 4096 || json.Unmarshal(data, &mode) != nil || mode.WingID != control.WingID {
		return ""
	}
	if mode.Mode == ModeLocalOnly || mode.Mode == ModeRelay {
		return mode.Mode
	}
	return ""
}

// RestartGuidance intentionally emits minimal commands rather than replaying
// flags whose ownership, quoting or arity may no longer be known.
func RestartGuidance(mode string, lan bool) string {
	local, relay := "wt wing start --local-only", "wt wing start"
	if lan {
		local += " --listen 0.0.0.0"
		relay += " --listen 0.0.0.0"
	}
	note := "Other flags the wing was started with must be added by hand."
	switch mode {
	case ModeLocalOnly:
		return local + "\n" + note
	case ModeRelay:
		return relay + "\n" + note
	default:
		return "The running wing's mode is unavailable or ambiguous; choose the variant matching its intended mode.\n" + local + "\n" + relay + "\n" + note
	}
}
