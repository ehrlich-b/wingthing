package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/memory"
	"github.com/ehrlich-b/wingthing/internal/skill"
	"github.com/ehrlich-b/wingthing/internal/store"
)

const overheadMargin = 1000

// ThreadRenderer renders the daily thread within a character budget.
// Implemented by the thread package.
type ThreadRenderer interface {
	Render(ctx context.Context, s *store.Store, date time.Time, budget int) string
}

// Builder assembles prompts for task execution.
type Builder struct {
	Store    *store.Store
	Memory   *memory.MemoryStore
	Config   *config.Config
	Agents   map[string]agent.Agent
	Thread   ThreadRenderer
}

// PromptResult holds the assembled prompt and metadata about what was included.
type PromptResult struct {
	Prompt       string
	Agent        string
	Isolation    string
	Mounts       []string
	Timeout      time.Duration
	MemoryLoaded []string
	BudgetUsed   int
	BudgetTotal  int
}

// Build assembles the full prompt for a task.
func (b *Builder) Build(ctx context.Context, taskID string) (*PromptResult, error) {
	// 1. Read task from store
	task, err := b.Store.GetTask(taskID)
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}
	if task == nil {
		return nil, fmt.Errorf("task not found: %s", taskID)
	}

	// 2. Load skill if type=skill
	var sk *skill.Skill
	if task.Type == "skill" {
		skillPath := filepath.Join(b.Config.SkillsDir(), task.What+".md")
		sk, err = skill.Load(skillPath)
		if err != nil {
			return nil, fmt.Errorf("load skill %s: %w", task.What, err)
		}
	}

	// 3. Resolve config (skill > agent defaults > config.yaml)
	agentIsolation := ""
	dbAgent, _ := b.Store.GetAgent(task.Agent)
	if dbAgent != nil {
		agentIsolation = dbAgent.DefaultIsolation
	}
	rc := ResolveConfig(sk, task.Agent, agentIsolation, b.Config)

	// 4. Look up agent context window
	contextWindow := 200000 // default
	if a, ok := b.Agents[rc.Agent]; ok {
		contextWindow = a.ContextWindow()
	}

	// 5. Compute budget
	taskLen := len(task.What)
	if sk != nil {
		taskLen = len(sk.Body)
	}
	budget := contextWindow - taskLen - overheadMargin - len(FormatDocs)
	if budget < 0 {
		budget = 0
	}

	// 6. Load memory
	var skillDeps []string
	if sk != nil {
		skillDeps = sk.Memory
	}

	// For ad-hoc tasks, always include identity
	if task.Type == "prompt" {
		hasIdentity := false
		for _, d := range skillDeps {
			if d == "identity" {
				hasIdentity = true
				break
			}
		}
		if !hasIdentity {
			skillDeps = append(skillDeps, "identity")
		}
	}

	// Also include task-level memory declarations
	if task.Memory != nil {
		var taskMem []string
		if err := json.Unmarshal([]byte(*task.Memory), &taskMem); err == nil {
			for _, m := range taskMem {
				found := false
				for _, d := range skillDeps {
					if d == m {
						found = true
						break
					}
				}
				if !found {
					skillDeps = append(skillDeps, m)
				}
			}
		}
	}

	entries := b.Memory.Retrieve(task.What, skillDeps)

	var memoryLoaded []string
	var memorySections []string
	for _, e := range entries {
		memoryLoaded = append(memoryLoaded, e.Name)
		if e.Body != "" {
			memorySections = append(memorySections, e.Body)
		}
	}

	memoryBlock := strings.Join(memorySections, "\n\n")
	budget -= len(memoryBlock)
	if budget < 0 {
		budget = 0
	}

	// 7. If skill: interpolate template
	var taskPrompt string
	if sk != nil {
		// Build data map for interpolation
		memMap := make(map[string]string)
		for _, e := range entries {
			memMap[e.Name] = e.Body
		}

		identityFM := b.Memory.Frontmatter("identity")
		idMap := make(map[string]string)
		if identityFM != nil {
			for k, v := range identityFM {
				idMap[k] = fmt.Sprintf("%v", v)
			}
		}

		// Render thread for interpolation
		threadStr := ""
		if b.Thread != nil && budget > 0 {
			threadStr = b.Thread.Render(ctx, b.Store, time.Now(), budget)
			budget -= len(threadStr)
			if budget < 0 {
				budget = 0
			}
		}

		idata := skill.InterpolateData{
			Memory:   memMap,
			Identity: idMap,
			Thread:   threadStr,
			Task:     task.What,
		}
		taskPrompt, _ = skill.Interpolate(sk.Body, idata)
	} else {
		// Ad-hoc: render thread, use task.What as the prompt
		threadStr := ""
		if b.Thread != nil && budget > 0 {
			threadStr = b.Thread.Render(ctx, b.Store, time.Now(), budget)
			budget -= len(threadStr)
			if budget < 0 {
				budget = 0
			}
		}

		var parts []string
		if threadStr != "" {
			parts = append(parts, "## Today So Far\n"+threadStr)
		}
		parts = append(parts, task.What)
		taskPrompt = strings.Join(parts, "\n\n")
	}

	// 8. Assemble final prompt
	var sections []string
	if memoryBlock != "" {
		sections = append(sections, memoryBlock)
	}
	sections = append(sections, taskPrompt)
	sections = append(sections, FormatDocs)

	prompt := strings.Join(sections, "\n\n")
	budgetUsed := len(prompt)

	var mounts []string
	if sk != nil {
		mounts = sk.Mounts
	}

	return &PromptResult{
		Prompt:       prompt,
		Agent:        rc.Agent,
		Isolation:    rc.Isolation,
		Mounts:       mounts,
		Timeout:      rc.Timeout,
		MemoryLoaded: memoryLoaded,
		BudgetUsed:   budgetUsed,
		BudgetTotal:  contextWindow,
	}, nil
}
