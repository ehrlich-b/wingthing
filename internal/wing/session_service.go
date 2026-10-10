package wing

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func configureSessionRegistration(sessions *wingsession.Service, ctx context.Context, routing *ws.PTYRegistry, write ws.PTYWriteFunc, passkeyPolicy func() auth.PasskeyPolicy, tools func() []*config.ToolConfig) func() {
	ctx, cancel := context.WithCancel(ctx)
	if write == nil {
		write = func(any) error { return nil }
	}
	var mu sync.Mutex
	var bridges sync.WaitGroup
	sessions.Register = func(id string) error {
		mu.Lock()
		defer mu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		// Web starts already have routing installed by the WebSocket client.
		// Every other start installs exactly the same bridge before acknowledgement.
		if routing.HasPTYSession(id) {
			return nil
		}
		dir := filepath.Join(sessions.Config.Dir, "eggs", id)
		ec, err := egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
		if err != nil {
			return err
		}
		input, cleanup, registered := routing.Register(id)
		if !registered {
			cmdutil.CloseWithLog("already registered session client", ec)
			return nil
		}
		policy := sessions.Policy()
		ttl, _ := time.ParseDuration(policy.Wing.AuthTTL)
		sessionTools := tools()
		bridges.Add(1)
		go func() {
			defer bridges.Done()
			defer cleanup()
			defer cmdutil.CloseWithLog("registered session client", ec)
			handleReclaimedPTY(ctx, sessions.Config, ec, id, dir, write, input, policy.Wing, policy.Keys, sessions.AuthCache, passkeyPolicy(), ttl, sessionTools, sessions)
		}()
		return nil
	}
	return func() {
		mu.Lock()
		cancel()
		mu.Unlock()
		bridges.Wait()
	}
}

// Reconcile before serving local control, rather than waiting for a relay
// registration. Register is idempotent and also reinstalls wing observers and
// privileged tool listeners for surviving eggs.
func reconcileEggSessions(ctx context.Context, sessions *wingsession.Service) error {
	active, err := sessions.Inventory(sessions.Config), ctx.Err()
	if err != nil {
		return err
	}
	for _, session := range active {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := sessions.Register(session.SessionID); err != nil {
			log.Printf("egg: local reclaim %s: %v", session.SessionID, err)
		}
	}
	return nil
}

func attachmentAuthority(wc *config.WingConfig, attach ws.PTYAttach) wingsession.Authority {
	home, _ := os.UserHomeDir()
	a := wingsession.AuthorityForWeb(wc, ws.TunnelRequest{SenderUserID: attach.UserID, SenderEmail: attach.Email, SenderOrgRole: attach.OrgRole}, home)
	a.PublicKey, a.AuthToken = attach.PublicKey, attach.AuthToken
	return a
}

func startAuthority(wc *config.WingConfig, start ws.PTYStart) wingsession.Authority {
	return attachmentAuthority(wc, ws.PTYAttach{UserID: start.UserID, Email: start.Email, OrgRole: start.OrgRole, PublicKey: start.PublicKey, AuthToken: start.AuthToken})
}
