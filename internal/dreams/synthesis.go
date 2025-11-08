package dreams

import (
	"fmt"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/memory"
)

// RunSynthesis performs the nightly Dreams synthesis
func RunSynthesis(cfg *config.Config, store *memory.Store) error {
	// TODO: Implement Dreams synthesis
	// 1. Get messages from last 24 hours
	// 2. Get existing memories for context
	// 3. Call LLM with synthesis prompt
	// 4. Parse response into Dream structure
	// 5. Save Dream to database
	// 6. Deliver Morning Card via Discord

	messages, err := store.GetRecentMessages(24)
	if err != nil {
		return fmt.Errorf("failed to get recent messages: %w", err)
	}

	if len(messages) == 0 {
		return fmt.Errorf("no messages in the last 24 hours")
	}

	// Placeholder: create a dummy Dream
	dream := &memory.Dream{
		Date:             time.Now().Format("2006-01-02"),
		YesterdaySummary: []string{"Placeholder summary"},
		OpenLoops:        []string{"Placeholder loop"},
		SupportPlan: []memory.SupportAction{
			{
				Action: "placeholder",
				Draft:  "This is a placeholder action",
			},
		},
	}

	if err := store.SaveDream(dream); err != nil {
		return fmt.Errorf("failed to save dream: %w", err)
	}

	return nil
}
