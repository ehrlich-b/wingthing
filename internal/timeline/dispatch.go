package timeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"encoding/json"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/cron"
	"github.com/ehrlich-b/wingthing/internal/parse"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/ehrlich-b/wingthing/internal/skill"
	"github.com/ehrlich-b/wingthing/internal/store"
)

func (e *Engine) dispatch(ctx context.Context, task *store.Task) error {
	// 1. Agent health check
	ag, ok := e.Agents[task.Agent]
	if !ok {
		return fmt.Errorf("agent %q not found", task.Agent)
	}
	if err := ag.Health(); err != nil {
		return fmt.Errorf("agent %q unhealthy: %w", task.Agent, err)
	}

	// 2. Build prompt via orchestrator
	pr, err := e.Builder.Build(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("build prompt: %w", err)
	}

	// 3. Log prompt_built event
	promptDetail := pr.Prompt
	e.Store.AppendLog(task.ID, "prompt_built", &promptDetail)

	// 4. Create sandbox
	sbCfg := sandbox.Config{
		Isolation: sandbox.ParseLevel(pr.Isolation),
	}
	sb, err := sandbox.New(sbCfg)
	if err != nil {
		return fmt.Errorf("sandbox: %w", err)
	}
	defer sb.Destroy()

	// 5. Execute agent
	timeout := 120 * time.Second
	agCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stream, err := ag.Run(agCtx, pr.Prompt, agent.RunOpts{Timeout: timeout})
	if err != nil {
		return fmt.Errorf("agent run: %w", err)
	}

	// 6. Capture full output
	for {
		_, more := stream.Next()
		if !more {
			break
		}
	}
	if err := stream.Err(); err != nil {
		return fmt.Errorf("stream interrupted: %w", err)
	}

	output := stream.Text()
	if output == "" {
		return fmt.Errorf("empty output")
	}

	e.Store.AppendLog(task.ID, "output_received", nil)

	// 7. Save output
	e.Store.SetTaskOutput(task.ID, output)

	// 8. Parse structured markers
	parsed := parse.Parse(output)
	for _, w := range parsed.Warnings {
		wMsg := w.Message
		e.Store.AppendLog(task.ID, "parse_warning", &wMsg)
	}
	e.Store.AppendLog(task.ID, "markers_parsed", nil)

	// 9. Process wt:schedule directives -> insert follow-up tasks
	for _, sd := range parsed.Schedules {
		runAt := time.Now()
		if sd.Delay > 0 {
			runAt = runAt.Add(sd.Delay)
		}
		if !sd.At.IsZero() {
			runAt = sd.At
		}
		followUp := &store.Task{
			ID:        fmt.Sprintf("%s-f%d", task.ID, runAt.UnixMilli()),
			Type:      "prompt",
			What:      sd.Content,
			RunAt:     runAt,
			Agent:     task.Agent,
			Isolation: task.Isolation,
			ParentID:  &task.ID,
			Status:    "pending",
		}
		if len(sd.Memory) > 0 {
			raw, _ := json.Marshal(sd.Memory)
			s := string(raw)
			followUp.Memory = &s
		}
		if err := e.Store.CreateTask(followUp); err != nil {
			msg := fmt.Sprintf("create follow-up: %v", err)
			e.Store.AppendLog(task.ID, "schedule_error", &msg)
		}
	}

	// 10. Process wt:memory directives (only if skill has memory_write: true)
	if e.MemoryDir != "" && task.Type == "skill" {
		sk, skErr := skill.Load(filepath.Join(e.Config.SkillsDir(), task.What+".md"))
		if skErr == nil && sk.MemoryWrite {
			for _, md := range parsed.Memories {
				memPath := filepath.Join(e.MemoryDir, md.File+".md")
				if err := os.WriteFile(memPath, []byte(md.Content), 0644); err != nil {
					msg := fmt.Sprintf("write memory %s: %v", md.File, err)
					e.Store.AppendLog(task.ID, "memory_write_error", &msg)
				}
			}
		}
	}

	// 11. Append thread entry
	summary := output
	if len(summary) > 200 {
		summary = summary[:200]
	}
	machineID := ""
	if e.Config != nil {
		machineID = e.Config.MachineID
	}
	var agentName *string
	if task.Agent != "" {
		agentName = &task.Agent
	}
	var skillName *string
	if task.Type == "skill" {
		skillName = &task.What
	}
	var tokensUsed *int
	if inTok, outTok := stream.Tokens(); inTok+outTok > 0 {
		total := inTok + outTok
		tokensUsed = &total
	}
	entry := &store.ThreadEntry{
		TaskID:     &task.ID,
		MachineID:  machineID,
		Agent:      agentName,
		Skill:      skillName,
		Summary:    summary,
		TokensUsed: tokensUsed,
	}
	e.Store.AppendThread(entry)
	e.Store.AppendLog(task.ID, "thread_appended", nil)

	// 12. Mark task done
	e.Store.UpdateTaskStatus(task.ID, "done")
	e.Store.AppendLog(task.ID, "completed", nil)

	// 13. Cron re-schedule: if task has a cron expression, create next occurrence
	if task.Cron != nil && *task.Cron != "" {
		sched, cronErr := cron.Parse(*task.Cron)
		if cronErr != nil {
			msg := fmt.Sprintf("cron parse: %v", cronErr)
			e.Store.AppendLog(task.ID, "cron_error", &msg)
		} else {
			nextRun := sched.Next(time.Now())
			if !nextRun.IsZero() {
				nextTask := &store.Task{
					ID:        fmt.Sprintf("%s-c%d", task.ID, nextRun.UnixMilli()),
					Type:      task.Type,
					What:      task.What,
					RunAt:     nextRun,
					Agent:     task.Agent,
					Isolation: task.Isolation,
					Memory:    task.Memory,
					Cron:      task.Cron,
					MachineID: task.MachineID,
					ParentID:  &task.ID,
					Status:    "pending",
				}
				if err := e.Store.CreateTask(nextTask); err != nil {
					msg := fmt.Sprintf("cron reschedule: %v", err)
					e.Store.AppendLog(task.ID, "cron_error", &msg)
				} else {
					msg := fmt.Sprintf("next run: %s (task %s)", nextRun.Format(time.RFC3339), nextTask.ID)
					e.Store.AppendLog(task.ID, "cron_scheduled", &msg)
				}
			}
		}
	}

	return nil
}
