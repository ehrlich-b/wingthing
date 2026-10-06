package localrelay

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/relay"
)

func EnvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func RelayPolicyFromEnv() (string, time.Time, error) {
	policy := strings.TrimSpace(EnvOr("WT_RELAY_POLICY", relay.RelayPolicyLegacy))
	if policy != relay.RelayPolicyLegacy && policy != relay.RelayPolicyDirectFree {
		return "", time.Time{}, fmt.Errorf("WT_RELAY_POLICY must be %q or %q", relay.RelayPolicyLegacy, relay.RelayPolicyDirectFree)
	}

	// The migration boundary is deployment state, not a compile-time product
	// default. Prefer the accurately named variable while retaining the old one
	// as a compatibility alias for existing operators.
	raw := strings.TrimSpace(os.Getenv("WT_RELAY_MIGRATION_BEFORE"))
	legacyRaw := strings.TrimSpace(os.Getenv("WT_RELAY_GRANDFATHER_BEFORE"))
	if raw != "" && legacyRaw != "" && raw != legacyRaw {
		return "", time.Time{}, fmt.Errorf("WT_RELAY_MIGRATION_BEFORE and deprecated WT_RELAY_GRANDFATHER_BEFORE disagree")
	}
	if raw == "" {
		raw = legacyRaw
	}
	if raw == "" {
		return policy, time.Time{}, nil
	}
	cutoff, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("WT_RELAY_MIGRATION_BEFORE must be RFC3339: %w", err)
	}
	return policy, cutoff, nil
}

func SaveLocalServeToken(configDir, token string) error {
	return auth.NewLocalTokenStore(configDir).Save(&auth.DeviceToken{
		Token:    token,
		DeviceID: "local",
	})
}

type serveRuntime struct {
	Config       *config.Config
	NodeRole     string
	LoginAddr    string
	FlyMachineID string
	FlyRegion    string
	FlyApp       string
	AutoRole     bool
	AutoLogin    bool
}

func LoadServeRuntime(flyDataDir string) (*serveRuntime, error) {
	runtime := &serveRuntime{
		NodeRole:     os.Getenv("WT_NODE_ROLE"),
		LoginAddr:    os.Getenv("WT_LOGIN_ADDR"),
		FlyMachineID: os.Getenv("FLY_MACHINE_ID"),
		FlyRegion:    os.Getenv("FLY_REGION"),
		FlyApp:       os.Getenv("FLY_APP_NAME"),
	}

	// Detect an unmounted Fly edge before config.Load can create its state
	// directory under /data and make that edge look like the volume-owning login
	// process. Explicit WT_NODE_ROLE always wins.
	if runtime.FlyMachineID != "" && runtime.NodeRole == "" {
		if info, err := os.Stat(flyDataDir); err == nil && info.IsDir() {
			runtime.NodeRole = "login"
		} else {
			runtime.NodeRole = "edge"
		}
		runtime.AutoRole = true
	}

	if runtime.NodeRole == "edge" && runtime.LoginAddr == "" && runtime.FlyApp != "" {
		runtime.LoginAddr = "http://login.process." + runtime.FlyApp + ".internal:8080"
		runtime.AutoLogin = true
	}

	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	runtime.Config = cfg
	return runtime, nil
}

// jwtKeyFromEnvironment prefers an explicit encoded P-256 key. Existing deployments that
// provide only WT_JWT_SECRET get a stable, domain-separated P-256 key without another secret.
func JwtKeyFromEnvironment() (string, error) {
	if key := os.Getenv("WT_JWT_KEY"); key != "" {
		return key, nil
	}
	if secret := os.Getenv("WT_JWT_SECRET"); secret != "" {
		return relay.DeriveECKeyStringFromSecret(secret)
	}
	return "", nil
}

// ensureJWTKeyInWingYaml loads the JWT signing key from wing.yaml, or generates
// one and saves it. Used by local/roost mode where there's no external secrets manager.
func EnsureJWTKeyInWingYaml(configDir string) (string, error) {
	wingCfg, err := config.LoadWingConfig(configDir)
	if err != nil {
		return "", fmt.Errorf("load wing config: %w", err)
	}

	if wingCfg.JWTKey != "" {
		return wingCfg.JWTKey, nil
	}

	// Auto-generate and persist
	_, encoded, err := relay.GenerateECKey()
	if err != nil {
		return "", err
	}

	wingCfg.JWTKey = encoded
	if err := config.SaveWingConfig(configDir, wingCfg); err != nil {
		return "", fmt.Errorf("save wing config: %w", err)
	}
	fmt.Println("generated JWT signing key → wing.yaml")
	return encoded, nil
}
