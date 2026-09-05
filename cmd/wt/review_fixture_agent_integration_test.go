//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

func runReviewFixtureAgent(args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("codex deterministic-wingthing-fixture")
		return 0
	}
	var model, prompt string
	for i, arg := range args {
		if arg == "-m" && i+1 < len(args) {
			model = args[i+1]
		}
		if strings.Contains(arg, "WT_REVIEW_FIXTURE=") {
			prompt = arg
		}
	}
	_, scenario, ok := strings.Cut(prompt, "WT_REVIEW_FIXTURE=")
	if !ok || len(strings.Fields(scenario)) == 0 {
		return 90
	}
	scenario = strings.Fields(scenario)[0]
	emit := func(value any) { _ = json.NewEncoder(os.Stdout).Encode(value) }
	emit(map[string]any{"type": "turn.started"})
	value, err := os.ReadFile("value.txt")
	if err != nil {
		return 91
	}
	output := "fixture implementation completed"
	switch model {
	case "fixture-terra":
		delay := 150 * time.Millisecond
		if scenario == "lease" {
			delay = 5 * time.Second
		} else if scenario == "timeout" {
			delay = 15 * time.Second
		} else if scenario == "transient" || scenario == "revoked" || scenario == "ambiguous" {
			delay = time.Second
		}
		time.Sleep(delay)
		next := "good\n"
		if scenario == "revision-limit" || ((scenario == "failed-tests" || scenario == "revision") && string(value) == "baseline\n") {
			next = "bad\n"
		}
		if err := os.WriteFile("value.txt", []byte(next), 0644); err != nil {
			return 92
		}
	case "fixture-sol":
		match := regexp.MustCompile(`"patch_sha256":"([0-9a-f]{64})"`).FindStringSubmatch(prompt)
		if len(match) != 2 {
			return 93
		}
		verdict := "pass"
		if (scenario == "revision" || scenario == "revision-limit") && string(value) == "bad\n" {
			verdict = "changes_requested"
		}
		review, _ := json.Marshal(map[string]string{"verdict": verdict, "patch_sha256": match[1], "summary": "fixture requires good value"})
		output = string(review)
	default:
		return 94
	}
	emit(map[string]any{"type": "item.completed", "item": map[string]string{"type": "agent_message", "text": output}})
	emit(map[string]any{"type": "turn.completed", "usage": map[string]int{"input_tokens": 0, "output_tokens": 0}})
	return 0
}
