package discord

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/dreams"
	"github.com/ehrlich-b/wingthing/internal/llm"
	"github.com/ehrlich-b/wingthing/internal/memory"
)

// Bot represents the Discord bot
type Bot struct {
	session  *discordgo.Session
	cfg      *config.Config
	store    *memory.Store
	llm      llm.Provider
}

// NewBot creates a new Discord bot
func NewBot(cfg *config.Config, store *memory.Store) (*Bot, error) {
	session, err := discordgo.New("Bot " + cfg.Discord.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to create Discord session: %w", err)
	}

	// Initialize LLM provider
	provider, err := llm.NewProvider(cfg.LLM.Provider, cfg.LLM.APIKey, cfg.LLM.Model)
	if err != nil {
		return nil, fmt.Errorf("failed to create LLM provider: %w", err)
	}

	bot := &Bot{
		session: session,
		cfg:     cfg,
		store:   store,
		llm:     provider,
	}

	// Register message handler
	session.AddHandler(bot.messageHandler)

	return bot, nil
}

// Start starts the Discord bot
func (b *Bot) Start() error {
	if err := b.session.Open(); err != nil {
		return fmt.Errorf("failed to open Discord session: %w", err)
	}

	log.Println("Discord bot connected successfully")

	// Send startup message
	b.sendDM("👋 WingThing is online! Send me a message anytime.")

	return nil
}

// Stop stops the Discord bot
func (b *Bot) Stop() error {
	return b.session.Close()
}

// messageHandler handles incoming Discord messages
func (b *Bot) messageHandler(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Ignore messages from the bot itself
	if m.Author.ID == s.State.User.ID {
		return
	}

	// Only respond to the configured user in DMs
	if m.Author.ID != b.cfg.Discord.UserID {
		return
	}

	// Only handle DMs
	channel, err := s.Channel(m.ChannelID)
	if err != nil || channel.Type != discordgo.ChannelTypeDM {
		return
	}

	// Save message to database
	msg := &memory.Message{
		DiscordID: m.ID,
		UserID:    m.Author.ID,
		Content:   m.Content,
		Timestamp: time.Now(),
	}
	if err := b.store.SaveMessage(msg); err != nil {
		log.Printf("Failed to save message: %v", err)
		return
	}

	// Handle commands
	if len(m.Content) > 0 && m.Content[0] == '/' {
		b.handleCommand(s, m)
		return
	}

	// Generate response using LLM
	if err := b.respondWithLLM(m.Content); err != nil {
		log.Printf("Failed to generate LLM response: %v", err)
		b.sendDM("Sorry, I had trouble processing that. Please try again.")
	}
}

// handleCommand processes slash commands
func (b *Bot) handleCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	switch m.Content {
	case "/start":
		b.sendDM("👋 Hi! I'm WingThing, your support companion.\n\n" +
			"Talk to me throughout the day, and each night I'll synthesize our conversations " +
			"into a Morning Card with actionable support moves.\n\n" +
			"Available commands:\n" +
			"• `/help` - Show this message\n" +
			"• `/stats` - View your stats\n" +
			"• `/dream` - Manually trigger Dreams synthesis")

	case "/help":
		b.sendDM("**WingThing Commands:**\n\n" +
			"• `/start` - Introduction and setup\n" +
			"• `/help` - Show this help message\n" +
			"• `/stats` - View message count and recent activity\n" +
			"• `/dream` - Manually run Dreams synthesis\n\n" +
			"Just chat with me naturally - I'm here to listen and support you!")

	case "/stats":
		messages, err := b.store.GetRecentMessages(24)
		if err != nil {
			b.sendDM("❌ Failed to get stats")
			return
		}

		dream, err := b.store.GetLatestDream()
		if err != nil {
			b.sendDM("❌ Failed to get latest Dream")
			return
		}

		stats := fmt.Sprintf("📊 **Your Stats:**\n\n"+
			"Messages in last 24h: %d\n", len(messages))

		if dream != nil {
			stats += fmt.Sprintf("Last Dream: %s\n", dream.Date)
			if dream.DeliveredAt != nil {
				stats += fmt.Sprintf("Delivered: %s\n", dream.DeliveredAt.Format("Jan 2 at 3:04pm"))
			}
		} else {
			stats += "No Dreams yet - keep chatting!\n"
		}

		b.sendDM(stats)

	case "/dream":
		b.sendDM("🌙 Running Dreams synthesis...")
		go b.runDreamsAsync()

	default:
		b.sendDM("Unknown command. Try `/help` to see available commands.")
	}
}

// respondWithLLM generates a response using the LLM and sends it
func (b *Bot) respondWithLLM(userMessage string) error {
	// Get recent conversation history for context
	messages, err := b.store.GetRecentMessages(24)
	if err != nil {
		return fmt.Errorf("failed to get message history: %w", err)
	}

	// Build conversation context
	llmMessages := []llm.Message{
		{
			Role: "system",
			Content: `You are WingThing, a supportive AI companion. Your role is to:
- Listen empathetically and validate feelings
- Provide emotional support (NOT therapy or clinical advice)
- Help people maintain connections with others
- Be warm, genuine, and non-judgmental
- Keep responses concise and conversational
- Remember context from the conversation

You are NOT a therapist. You are a supportive friend who helps people feel heard and helps them maintain their human relationships.`,
		},
	}

	// Add recent message history (last 10 messages for context)
	startIdx := 0
	if len(messages) > 10 {
		startIdx = len(messages) - 10
	}
	for _, msg := range messages[startIdx:] {
		role := "assistant"
		if msg.UserID == b.cfg.Discord.UserID {
			role = "user"
		}
		llmMessages = append(llmMessages, llm.Message{
			Role:    role,
			Content: msg.Content,
		})
	}

	// Call LLM
	ctx := context.Background()
	response, err := b.llm.Chat(ctx, llmMessages)
	if err != nil {
		return fmt.Errorf("LLM chat failed: %w", err)
	}

	// Send response
	if err := b.sendDM(response); err != nil {
		return fmt.Errorf("failed to send response: %w", err)
	}

	// Save bot's response to database
	botMsg := &memory.Message{
		DiscordID: fmt.Sprintf("bot-%d", time.Now().Unix()),
		UserID:    "bot",
		Content:   response,
		Timestamp: time.Now(),
	}
	if err := b.store.SaveMessage(botMsg); err != nil {
		log.Printf("Warning: failed to save bot message: %v", err)
	}

	return nil
}

// sendDM sends a DM to the configured user
func (b *Bot) sendDM(content string) error {
	// Create DM channel with the user
	channel, err := b.session.UserChannelCreate(b.cfg.Discord.UserID)
	if err != nil {
		return fmt.Errorf("failed to create DM channel: %w", err)
	}

	// Send message
	_, err = b.session.ChannelMessageSend(channel.ID, content)
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	return nil
}

// SendMorningCard sends a formatted Morning Card
func (b *Bot) SendMorningCard(dream *memory.Dream) error {
	// Create rich embed for Morning Card
	embed := &discordgo.MessageEmbed{
		Title:       "☕ Morning Card",
		Description: "Here's your support plan for today",
		Color:       0x5865F2, // Blurple
		Timestamp:   time.Now().Format(time.RFC3339),
		Fields:      []*discordgo.MessageEmbedField{},
	}

	// Add yesterday summary
	if len(dream.YesterdaySummary) > 0 {
		summary := ""
		for _, item := range dream.YesterdaySummary {
			summary += "• " + item + "\n"
		}
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "📝 Yesterday",
			Value:  summary,
			Inline: false,
		})
	}

	// Add open loops
	if len(dream.OpenLoops) > 0 {
		loops := ""
		for _, item := range dream.OpenLoops {
			loops += "• " + item + "\n"
		}
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "🔄 Open Loops",
			Value:  loops,
			Inline: false,
		})
	}

	// Add support plan
	if len(dream.SupportPlan) > 0 {
		plan := ""
		for i, action := range dream.SupportPlan {
			plan += fmt.Sprintf("%d. **%s**\n   %s\n\n", i+1, action.Action, action.Draft)
		}
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "💪 Your Support Plan",
			Value:  plan,
			Inline: false,
		})
	}

	embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
		Name:   "✨",
		Value:  "You got this. 💙",
		Inline: false,
	})

	// Create DM channel
	channel, err := b.session.UserChannelCreate(b.cfg.Discord.UserID)
	if err != nil {
		return fmt.Errorf("failed to create DM channel: %w", err)
	}

	// Send embed
	_, err = b.session.ChannelMessageSendEmbed(channel.ID, embed)
	if err != nil {
		return fmt.Errorf("failed to send Morning Card: %w", err)
	}

	return nil
}

// runDreamsAsync runs Dreams synthesis and delivers the Morning Card
func (b *Bot) runDreamsAsync() {
	// Run synthesis
	if err := dreams.RunSynthesis(b.cfg, b.store); err != nil {
		log.Printf("Dreams synthesis failed: %v", err)
		b.sendDM("❌ Dreams synthesis failed. Check logs for details.")
		return
	}

	// Get the latest Dream
	dream, err := b.store.GetLatestDream()
	if err != nil {
		log.Printf("Failed to get latest dream: %v", err)
		b.sendDM("❌ Failed to retrieve Morning Card.")
		return
	}

	if dream == nil {
		b.sendDM("❌ No Morning Card generated.")
		return
	}

	// Send Morning Card
	if err := b.SendMorningCard(dream); err != nil {
		log.Printf("Failed to send Morning Card: %v", err)
		b.sendDM("❌ Failed to deliver Morning Card.")
		return
	}

	// Mark as delivered
	if err := b.store.MarkDreamDelivered(dream.ID); err != nil {
		log.Printf("Warning: failed to mark dream as delivered: %v", err)
	}

	log.Printf("Morning Card delivered successfully for %s", dream.Date)
}
