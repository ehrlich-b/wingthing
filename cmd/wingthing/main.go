package main

import (
	"context"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/behrlich/wingthing/internal/ui"
)

var (
	prompt    string
	jsonMode  bool
	maxTurns  int
	resume    bool
)

func main() {
	var rootCmd = &cobra.Command{
		Use:   "wingthing",
		Short: "A Claude Code competitor built with Bubble Tea",
		Long:  "An interactive terminal application for AI-assisted development",
		RunE:  run,
	}

	rootCmd.Flags().StringVarP(&prompt, "prompt", "p", "", "One-shot prompt (headless mode)")
	rootCmd.Flags().BoolVar(&jsonMode, "json", false, "Stream structured JSON events")
	rootCmd.Flags().IntVar(&maxTurns, "max-turns", 0, "Cap agent loops")
	rootCmd.Flags().BoolVar(&resume, "resume", false, "Load last session from local history")

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Headless mode with prompt flag
	if prompt != "" {
		return runHeadless(ctx, prompt)
	}

	// Interactive Bubble Tea UI
	return runInteractive(ctx)
}

func runHeadless(ctx context.Context, prompt string) error {
	// TODO: Implement headless mode with JSON output
	fmt.Printf(`{"type":"plan","content":"Processing prompt: %s"}%s`, prompt, "\n")
	fmt.Printf(`{"type":"final","content":"Headless mode not yet implemented"}%s`, "\n")
	return nil
}

func runInteractive(ctx context.Context) error {
	model := ui.NewModel()
	
	p := tea.NewProgram(
		model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	_, err := p.Run()
	return err
}
