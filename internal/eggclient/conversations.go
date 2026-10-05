package eggclient

import (
	"os"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"

	"github.com/ehrlich-b/wingthing/internal/store"
)

type ConversationLink struct {
	ConversationID       string `json:"conversation_id,omitempty"`
	RootConversationID   string `json:"root_conversation_id,omitempty"`
	ParentConversationID string `json:"parent_conversation_id,omitempty"`
	ConversationRole     string `json:"conversation_role,omitempty"`
}

func linkForConversation(c *store.Conversation) ConversationLink {
	if c == nil {
		return ConversationLink{}
	}
	role := "child"
	if c.ParentID == "" {
		role = "parent"
	}
	return ConversationLink{c.ID, c.RootID, c.ParentID, role}
}

func SessionConversationLink(cfg *config.Config, session string) ConversationLink {
	// Do not create or migrate state just to enrich legacy session inventory.
	if _, err := os.Stat(cfg.DBPath()); err != nil {
		return ConversationLink{}
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return ConversationLink{}
	}
	defer cmdutil.CloseWithLog("conversation inventory store", db)
	c, err := db.ConversationForSession(session)
	if err != nil {
		return ConversationLink{}
	}
	return linkForConversation(c)
}
