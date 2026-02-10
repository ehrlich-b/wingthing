package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/spf13/cobra"
)

var wellKnownEnvKeys = []struct {
	name    string
	envVar  string
	enables string
}{
	{"openai", "OPENAI_API_KEY", "embeddings, agents"},
	{"anthropic", "ANTHROPIC_API_KEY", "agents"},
	{"google", "GEMINI_API_KEY", "agents"},
	{"google", "GOOGLE_API_KEY", "agents"},
}

var wellKnownCLIs = []struct {
	name    string
	cmd     string
	enables string
}{
	{"claude", "claude", "agents"},
	{"ollama", "ollama", "agents, embeddings"},
	{"gemini", "gemini", "agents"},
	{"codex", "codex", "agents"},
	{"cursor", "agent", "agents"},
}

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check available agents, embedders, and API keys",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			fmt.Println("wingthing doctor")
			fmt.Println()

			// CLI tools
			fmt.Println("CLI tools:")
			for _, c := range wellKnownCLIs {
				path, err := exec.LookPath(c.cmd)
				if err != nil {
					fmt.Printf("  %-12s not found\n", c.name)
				} else {
					fmt.Printf("  %-12s %s\n", c.name, path)
				}
			}
			fmt.Println()

			// API keys
			fmt.Println("API keys:")
			for _, k := range wellKnownEnvKeys {
				val := lookupEnv(k.envVar)
				if val != "" {
					fmt.Printf("  %-20s set (%s)\n", k.envVar, k.enables)
				} else {
					fmt.Printf("  %-20s not set\n", k.envVar)
				}
			}
			fmt.Println()

			// Services
			fmt.Println("Services:")
			ollamaURL := "http://localhost:11434"
			if ollamaReachable(ollamaURL) {
				fmt.Printf("  %-12s reachable at %s\n", "ollama", ollamaURL)
			} else {
				fmt.Printf("  %-12s not reachable\n", "ollama")
			}
			if cfg.RelayURL != "" {
				if relayReachable(cfg.RelayURL) {
					fmt.Printf("  %-12s reachable at %s\n", "relay", cfg.RelayURL)
				} else {
					fmt.Printf("  %-12s not reachable at %s\n", "relay", cfg.RelayURL)
				}
			}
			fmt.Println()

			// Config
			fmt.Println("Config:")
			fmt.Printf("  dir:              %s\n", cfg.Dir)
			fmt.Printf("  default_agent:    %s\n", cfg.DefaultAgent)
			fmt.Printf("  default_embedder: %s\n", cfg.DefaultEmbedder)
			if cfg.RelayURL != "" {
				fmt.Printf("  relay_url:        %s\n", cfg.RelayURL)
			}

			return nil
		},
	}
}

func ollamaReachable(baseURL string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(baseURL + "/api/tags")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func lookupEnv(key string) string {
	return os.Getenv(key)
}

func relayReachable(relayURL string) bool {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(relayURL + "/health")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
