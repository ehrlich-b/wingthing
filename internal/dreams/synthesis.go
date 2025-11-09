package dreams

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/llm"
	"github.com/ehrlich-b/wingthing/internal/logger"
	"github.com/ehrlich-b/wingthing/internal/memory"
)

const dreamsPrompt = `You are WingThing's Dreams processor. Every night, you synthesize the day's conversations into a supportive Morning Card.

Your job:
1. Review yesterday's conversations
2. Identify what matters (feelings, challenges, open tasks, people mentioned)
3. Create a Morning Card with 3 specific support actions for tomorrow

Guidelines:
- Focus on emotional support, not therapy
- Be concise and actionable
- Remember people and relationships mentioned
- Notice patterns (stress, overwhelm, joy)
- Suggest small, concrete support actions

Output format (JSON):
{
  "yesterday_summary": ["bullet 1", "bullet 2", "bullet 3"],
  "open_loops": ["thing to remember", "pending item"],
  "support_plan": [
    {
      "action": "text_check_in",
      "target": "person_name",
      "draft": "Hey [person], thinking of you. How did [thing] go?"
    },
    {
      "action": "self_care",
      "draft": "Tonight: [specific suggestion based on their patterns]"
    },
    {
      "action": "prep",
      "draft": "Tomorrow tip: [specific helpful action]"
    }
  ]
}

Yesterday's conversations:
%s

Generate the Morning Card (JSON only, no explanation):
`

// RunSynthesis performs the nightly Dreams synthesis
func RunSynthesis(cfg *config.Config, store *memory.Store) error {
	logger.Info("Starting Dreams synthesis")

	// Get messages from last 24 hours
	messages, err := store.GetRecentMessages(24)
	if err != nil {
		return fmt.Errorf("failed to get recent messages: %w", err)
	}

	if len(messages) == 0 {
		logger.Warn("No messages in the last 24 hours, skipping Dreams synthesis")
		return fmt.Errorf("no messages in the last 24 hours")
	}

	logger.Debug("Retrieved messages for Dreams synthesis", "count", len(messages))

	// Build context from messages
	conversationContext := buildConversationContext(messages)
	logger.Debug("Built conversation context", "length", len(conversationContext))

	// Initialize LLM with gpt-5-mini for Dreams synthesis (cheaper)
	provider, err := llm.NewProvider(cfg.LLM.Provider, cfg.LLM.APIKey, "gpt-5-mini")
	if err != nil {
		return fmt.Errorf("failed to create LLM provider: %w", err)
	}

	// Call LLM with Dreams prompt
	prompt := fmt.Sprintf(dreamsPrompt, conversationContext)
	ctx := context.Background()

	logger.Info("Calling LLM for Dreams synthesis", "model", "gpt-5-mini")

	response, err := provider.Chat(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return fmt.Errorf("LLM synthesis failed: %w", err)
	}

	// Parse response into Dream structure
	dream, err := parseMorningCard(response)
	if err != nil {
		logger.Error("Failed to parse Morning Card", "error", err, "response", response)
		return fmt.Errorf("failed to parse Morning Card: %w", err)
	}

	// Set date
	dream.Date = time.Now().Format("2006-01-02")

	logger.Debug("Parsed Morning Card",
		"date", dream.Date,
		"summary_items", len(dream.YesterdaySummary),
		"open_loops", len(dream.OpenLoops),
		"support_actions", len(dream.SupportPlan))

	// Save Dream to database
	if err := store.SaveDream(dream); err != nil {
		return fmt.Errorf("failed to save dream: %w", err)
	}

	logger.Info("Dreams synthesis complete", "date", dream.Date)
	fmt.Printf("Dreams synthesis complete! Generated Morning Card for %s\n", dream.Date)
	return nil
}

// buildConversationContext formats messages for the Dreams prompt
func buildConversationContext(messages []memory.Message) string {
	var builder strings.Builder

	for _, msg := range messages {
		timestamp := msg.Timestamp.Format("3:04 PM")
		sender := "User"
		if msg.UserID == "bot" {
			sender = "WingThing"
		}
		builder.WriteString(fmt.Sprintf("[%s] %s: %s\n", timestamp, sender, msg.Content))
	}

	return builder.String()
}

// parseMorningCard extracts the Dream structure from LLM JSON response
func parseMorningCard(response string) (*memory.Dream, error) {
	// Try to extract JSON from response (LLM might add extra text)
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")

	if start == -1 || end == -1 {
		return nil, fmt.Errorf("no JSON found in response")
	}

	jsonStr := response[start : end+1]

	// Parse JSON
	var parsed struct {
		YesterdaySummary []string                `json:"yesterday_summary"`
		OpenLoops        []string                `json:"open_loops"`
		SupportPlan      []memory.SupportAction  `json:"support_plan"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	return &memory.Dream{
		YesterdaySummary: parsed.YesterdaySummary,
		OpenLoops:        parsed.OpenLoops,
		SupportPlan:      parsed.SupportPlan,
	}, nil
}
