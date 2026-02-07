package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/memory"
	"github.com/ehrlich-b/wingthing/internal/orchestrator"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/timeline"
	"github.com/ehrlich-b/wingthing/internal/transport"
)

type Daemon struct {
	Config *config.Config
	Store  *store.Store
}

func Run(cfg *config.Config) error {
	s, err := store.Open(cfg.DBPath())
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer s.Close()

	// Recover interrupted tasks from previous crash
	recoverInterrupted(s)

	// Set up agents
	agents := map[string]agent.Agent{
		"claude": agent.NewClaude(200000),
		"ollama": agent.NewOllama("", 128000),
	}
	// Register agents in store
	s.UpsertAgent(&store.Agent{
		Name:          "claude",
		Adapter:       "claude",
		Command:       "claude",
		ContextWindow: 200000,
	})
	s.UpsertAgent(&store.Agent{
		Name:          "ollama",
		Adapter:       "ollama",
		Command:       "ollama run llama3.2",
		ContextWindow: 128000,
	})

	mem := memory.New(cfg.MemoryDir())

	builder := &orchestrator.Builder{
		Store:  s,
		Memory: mem,
		Config: cfg,
		Agents: agents,
	}

	// Timeline engine
	engine := &timeline.Engine{
		Store:        s,
		Builder:      builder,
		Config:       cfg,
		Agents:       agents,
		PollInterval: time.Second,
		MemoryDir:    cfg.MemoryDir(),
	}

	// Transport server
	srv := transport.NewServer(s, cfg.SocketPath())
	srv.SetSkillsDir(cfg.SkillsDir())
	srv.DefaultMaxRetries = cfg.DefaultMaxRetries

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	errCh := make(chan error, 2)

	go func() {
		log.Println("timeline engine started")
		errCh <- engine.Run(ctx)
	}()

	go func() {
		log.Printf("transport listening on %s", cfg.SocketPath())
		errCh <- srv.ListenAndServe(ctx)
	}()

	log.Printf("wingthing daemon started (dir=%s)", cfg.Dir)

	select {
	case sig := <-sigCh:
		log.Printf("received %s, shutting down...", sig)
		cancel()
		// Grace period for running tasks
		time.Sleep(time.Second)
	case err := <-errCh:
		if err != nil && err != context.Canceled {
			cancel()
			return fmt.Errorf("daemon error: %w", err)
		}
	}

	return nil
}

func recoverInterrupted(s *store.Store) {
	rows, err := s.DB().Query("SELECT id FROM tasks WHERE status = 'running'")
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		rows.Scan(&id)
		s.SetTaskError(id, "daemon shutdown")
		log.Printf("recovered interrupted task: %s", id)
	}
}
