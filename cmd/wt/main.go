package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "wt",
		Short: "wingthing — local-first AI task runner",
		Long:  "Orchestrates LLM agents on your behalf. Manages context, memory, and task timelines.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			// bare string arg = ad-hoc prompt task
			fmt.Printf("would submit task: %s\n", args[0])
			return nil
		},
		Args: cobra.MaximumNArgs(1),
	}

	root.AddCommand(
		timelineCmd(),
		threadCmd(),
		statusCmd(),
		logCmd(),
		agentCmd(),
		skillCmd(),
		daemonCmd(),
		initCmd(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func timelineCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "timeline",
		Short: "Show upcoming and recent tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("not implemented")
			return nil
		},
	}
}

func threadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "thread",
		Short: "Print today's daily thread",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("not implemented")
			return nil
		},
	}
	cmd.Flags().Bool("yesterday", false, "Show yesterday's thread")
	return cmd
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Daemon status and agent health",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("not implemented")
			return nil
		},
	}
}

func logCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Show task log events",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("not implemented")
			return nil
		},
	}
	cmd.Flags().Bool("last", false, "Show most recent task")
	cmd.Flags().Bool("context", false, "Include full prompt")
	return cmd
}

func agentCmd() *cobra.Command {
	agent := &cobra.Command{
		Use:   "agent",
		Short: "Manage agent adapters",
	}
	agent.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List configured agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("not implemented")
			return nil
		},
	})
	return agent
}

func skillCmd() *cobra.Command {
	skill := &cobra.Command{
		Use:   "skill",
		Short: "Manage skills",
	}
	skill.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List installed skills",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("not implemented")
			return nil
		},
	})
	skill.AddCommand(&cobra.Command{
		Use:   "add [name|file|url]",
		Short: "Install a skill",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("not implemented")
			return nil
		},
	})
	return skill
}

func daemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Start the wingthing daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("not implemented")
			return nil
		},
	}
	cmd.Flags().Bool("install", false, "Install as system service")
	return cmd
}

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize ~/.wingthing directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("not implemented")
			return nil
		},
	}
}
