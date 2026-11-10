package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

func writeSessionHistoryJSON(out io.Writer, state string) error {
	wc, err := config.LoadWingConfig(state)
	if err != nil {
		return err
	}
	type historyRow struct {
		ID      string `json:"id"`
		Agent   string `json:"agent"`
		CWD     string `json:"cwd"`
		Context string `json:"context"`
	}
	rows := []historyRow{}
	entries, err := os.ReadDir(filepath.Join(state, "eggs"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(state, "eggs", entry.Name())
		if _, err := os.Stat(filepath.Join(dir, "chat.jsonl.gz")); err != nil {
			continue
		}
		agent, cwd := eggclient.ReadEggMeta(dir)
		if raw, err := os.ReadFile(filepath.Join(dir, "chat.meta")); err == nil {
			meta := egg.ParseChatMeta(string(raw))
			if meta["agent"] != "" {
				agent = meta["agent"]
			}
			if meta["cwd"] != "" {
				cwd = meta["cwd"]
			}
		}
		if agent == "" {
			agent = "unknown"
		}
		rows = append(rows, historyRow{entry.Name(), agent, cwd, wc.MachineContext})
	}
	return json.NewEncoder(out).Encode(rows)
}
