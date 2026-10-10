package egg

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// Session-scoped overrides suppress startup UI without changing provider files
// or trusting any hooks beyond the generated observers. Authentication still
// requires an existing login; an unexpected modal fails the readiness bound.
func codexStartupArgs(args []string, cwd string) []string {
	end := providerOptionsEnd(args)
	out := append([]string(nil), args[:end]...)
	for _, value := range []string{
		"check_for_update_on_startup=false",
		"notice={hide_full_access_warning=true,hide_gpt5_1_migration_prompt=true,\"hide_gpt-5.1-codex-max_migration_prompt\"=true,hide_rate_limit_model_nudge=true,hide_world_writable_warning=true,external_config_migration_prompts={home=true,projects={" + strconv.Quote(cwd) + "=true}}}",
		"projects={" + strconv.Quote(cwd) + "={trust_level=\"trusted\"}}",
	} {
		out = append(out, "-c", value)
	}
	return append(out, args[end:]...)
}

// Codex has no native empty-composer signal. This narrow screen fallback gates
// the first paste only; it never reports semantic readiness or completion.
func codexComposerReady(screen string) bool {
	screen = strings.ToLower(screen)
	for _, blocker := range []string{"update available", "sign in", "log in", "trust", "loading"} {
		if strings.Contains(screen, blocker) {
			return false
		}
	}
	return strings.Contains(screen, "openai codex") && strings.Contains(screen, "ask codex to do anything")
}

func (rt *runTurnRuntime) sendInitialCodex(ctx context.Context, input string, admitted <-chan struct{}) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-admitted:
			return nil
		default:
		}
		if rt.backend.StartupReady != nil && rt.backend.StartupReady() {
			delivery, err := rt.backend.InitialSend(ctx, input)
			if delivery.Release != nil {
				defer delivery.Release()
			}
			if err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-admitted:
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-admitted:
			return nil // Native receipt already observed: never paste twice.
		case <-ticker.C:
		}
	}
}

// Retain only allowlisted descriptions of the last rendered screen. Arbitrary
// screen words, paths, URLs, prompt text and credentials never enter errors.
func codexStartupDiagnostic(screen string) string {
	screen = strings.ToLower(screen)
	switch {
	case strings.Contains(screen, "update available"):
		return "Startup screen: update prompt (details redacted)."
	case strings.Contains(screen, "sign in") || strings.Contains(screen, "log in"):
		return "Startup screen: authentication required (details redacted)."
	case strings.Contains(screen, "trust"):
		return "Startup screen: trust prompt (details redacted)."
	case strings.Contains(screen, "ask codex"):
		return "Startup screen: composer visible; native prompt receipt missing (details redacted)."
	case strings.Contains(screen, "loading"):
		return "Startup screen: loading (details redacted)."
	default:
		return "Startup screen: unrecognized or unavailable (details redacted)."
	}
}
