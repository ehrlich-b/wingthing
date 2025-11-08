package discord

import (
	"fmt"
	"log"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/memory"
)

// Bot represents the Discord bot
type Bot struct {
	session *discordgo.Session
	cfg     *config.Config
	store   *memory.Store
}

// NewBot creates a new Discord bot
func NewBot(cfg *config.Config, store *memory.Store) (*Bot, error) {
	session, err := discordgo.New("Bot " + cfg.Discord.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to create Discord session: %w", err)
	}

	bot := &Bot{
		session: session,
		cfg:     cfg,
		store:   store,
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
	if m.Content[0] == '/' {
		b.handleCommand(s, m)
		return
	}

	// TODO: Generate response using LLM
	// For now, send a placeholder response
	b.sendDM("I hear you. (This is a placeholder - LLM integration coming soon!)")
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
		b.sendDM("🌙 Running Dreams synthesis... (not yet implemented)")

	default:
		b.sendDM("Unknown command. Try `/help` to see available commands.")
	}
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
