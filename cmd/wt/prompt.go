package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/promptmgr"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/spf13/cobra"
)

func promptCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prompt",
		Short: "Manage named, versioned prompt templates",
	}
	cmd.AddCommand(promptListCmd(), promptShowCmd(), promptSaveCmd(), promptRunCmd())
	return cmd
}

func promptListCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List saved prompts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			assets, err := promptmgr.New(cfg.PromptsDir()).List()
			if err != nil {
				return err
			}
			if jsonOutput {
				return writePromptJSON(cmd.OutOrStdout(), assets)
			}
			if len(assets) == 0 {
				return writeln(cmd.OutOrStdout(), "no saved prompts")
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			if err := writeln(w, "NAME\tREVISION\tAGENT\tVARIABLES\tDESCRIPTION"); err != nil {
				return err
			}
			for _, asset := range assets {
				if err := writef(w, "%s\t%s\t%s\t%s\t%s\n", asset.Name, asset.Revision, asset.Agent,
					strings.Join(asset.Variables, ","), asset.Description); err != nil {
					return err
				}
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print JSON")
	return cmd
}

func promptShowCmd() *cobra.Command {
	var revision string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "show NAME",
		Short: "Show a saved prompt or historical revision",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			asset, err := promptmgr.New(cfg.PromptsDir()).Get(args[0], revision)
			if err != nil {
				return err
			}
			if jsonOutput {
				return writePromptJSON(cmd.OutOrStdout(), asset)
			}
			out := cmd.OutOrStdout()
			if err := writef(out, "%s@%s\n", asset.Name, asset.Revision); err != nil {
				return err
			}
			if asset.Description != "" {
				if err := writef(out, "description: %s\n", asset.Description); err != nil {
					return err
				}
			}
			if asset.Agent != "" {
				if err := writef(out, "agent: %s\n", asset.Agent); err != nil {
					return err
				}
			}
			if asset.CWD != "" {
				if err := writef(out, "cwd: %s\n", asset.CWD); err != nil {
					return err
				}
			}
			if len(asset.Variables) > 0 {
				if err := writef(out, "variables: %s\n", strings.Join(asset.Variables, ", ")); err != nil {
					return err
				}
			}
			return writef(out, "\n%s\n", asset.Template)
		},
	}
	cmd.Flags().StringVar(&revision, "revision", "", "Read an immutable historical revision")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print JSON")
	return cmd
}

func promptSaveCmd() *cobra.Command {
	var description, templateBody, templateFile, agentName, cwd, expectedRevision string
	var variables []string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "save NAME",
		Short: "Create or update a versioned prompt",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (templateBody == "") == (templateFile == "") {
				return errors.New("provide exactly one of --template or --file")
			}
			if templateFile != "" {
				var data []byte
				var err error
				if templateFile == "-" {
					data, err = io.ReadAll(cmd.InOrStdin())
				} else {
					data, err = os.ReadFile(templateFile)
				}
				if err != nil {
					return fmt.Errorf("read prompt template: %w", err)
				}
				templateBody = string(data)
			}
			if cwd != "" {
				var err error
				cwd, err = filepath.Abs(cwd)
				if err != nil {
					return err
				}
			}
			if agentName != "" {
				if _, ok := agent.LookupDefinition(agentName); !ok {
					return fmt.Errorf("unsupported agent %q", agentName)
				}
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			asset, err := promptmgr.New(cfg.PromptsDir()).Save(promptmgr.Asset{
				Name: args[0], Description: description, Template: templateBody,
				Variables: variables, Agent: agentName, CWD: cwd,
			}, expectedRevision)
			if err != nil {
				return err
			}
			if jsonOutput {
				return writePromptJSON(cmd.OutOrStdout(), asset)
			}
			return writef(cmd.OutOrStdout(), "saved: %s@%s\n", asset.Name, asset.Revision)
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "Human-readable purpose")
	cmd.Flags().StringVar(&templateBody, "template", "", "Prompt template body")
	cmd.Flags().StringVar(&templateFile, "file", "", "Read template from a file, or - for stdin")
	cmd.Flags().StringSliceVar(&variables, "variable", nil, "Declared template variable (repeatable)")
	cmd.Flags().StringVar(&agentName, "agent", "", "Default supported agent")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Default absolute working directory")
	cmd.Flags().StringVar(&expectedRevision, "expected-revision", "", "Reject a conflicting update")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print JSON")
	return cmd
}

func promptRunCmd() *cobra.Command {
	var revision, agentName, cwd string
	var variables []string
	cmd := &cobra.Command{
		Use:   "run NAME",
		Short: "Render and execute a saved prompt",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			asset, err := promptmgr.New(cfg.PromptsDir()).Get(args[0], revision)
			if err != nil {
				return err
			}
			values, err := parsePromptValues(variables)
			if err != nil {
				return err
			}
			rendered, err := promptmgr.Render(asset, values)
			if err != nil {
				return err
			}
			if agentName == "" {
				agentName = asset.Agent
			}
			if cwd == "" {
				cwd = asset.CWD
			}
			cwd, err = resolveWorkingDirectory(cwd)
			if err != nil {
				return err
			}
			taskStore, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer closeWithLog("prompt task store", taskStore)
			task := &store.Task{
				ID: genTaskID(), Type: "prompt", What: rendered, Agent: agentName,
				RunAt: time.Now().UTC(), CWD: cwd, PromptName: asset.Name, PromptRevision: asset.Revision,
			}
			if err := taskStore.CreateTask(task); err != nil {
				return err
			}
			if err := writef(cmd.OutOrStdout(), "submitted: %s (%s@%s)\n", task.ID, asset.Name, asset.Revision); err != nil {
				return err
			}
			return runTaskTo(cmd.Context(), cfg, taskStore, task, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&revision, "revision", "", "Run an immutable historical revision")
	cmd.Flags().StringSliceVar(&variables, "var", nil, "Template value as name=value (repeatable)")
	cmd.Flags().StringVar(&agentName, "agent", "", "Override the prompt's default agent")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Override the prompt's working directory")
	return cmd
}

func parsePromptValues(entries []string) (map[string]string, error) {
	values := make(map[string]string, len(entries))
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("invalid prompt variable %q; want name=value", entry)
		}
		if _, exists := values[name]; exists {
			return nil, fmt.Errorf("duplicate prompt variable %q", name)
		}
		values[name] = value
	}
	return values, nil
}

func writePromptJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
