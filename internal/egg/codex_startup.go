package egg

import (
	"strconv"
	"strings"
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
