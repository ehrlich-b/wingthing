package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/spf13/cobra"
)

const maxPhoneLinkBytes = 16 << 10

func phoneCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "phone", Short: "Set up the phone app"}
	var jsonOutput, noToken bool
	link := &cobra.Command{
		Use: "link", Short: "Print a private setup link for this wing", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return errors.New("cannot read wing configuration")
			}
			store := auth.NewTokenStore(cfg.Dir)
			token, err := store.Load()
			if err != nil {
				// YAML and transport errors can contain credential text. Never
				// include them in CLI errors (which callers may log).
				return errors.New("cannot read device token")
			}
			if !store.IsValid(token) || strings.TrimSpace(token.Token) == "" {
				return errors.New("not logged in — run: wt login")
			}
			wing, err := config.LoadWingConfig(cfg.Dir)
			if err != nil {
				return errors.New("cannot read wing configuration")
			}
			origin := wing.Roost
			if origin == "" {
				origin = cfg.RoostURL
			}
			if origin == "" {
				origin = config.DefaultRelayURL()
			}
			origin = wingpolicy.NormalizeRelayHTTPURL(origin)
			if err := checkPhoneOrigin(origin); err != nil {
				return err
			}
			privateKey, err := auth.LoadPrivateKey(cfg.Dir)
			if err != nil {
				return errors.New("cannot read this wing's key — log in again")
			}
			key := base64.StdEncoding.EncodeToString(privateKey.PublicKey().Bytes())
			if token.PublicKey != "" && token.PublicKey != key {
				return errors.New("stored login does not match this wing's public key — log in again")
			}
			account := token.UserID
			if account == "" {
				account, err = fetchPhoneAccount(cmd.Context(), origin, token.Token)
				if err != nil {
					return err
				}
			}
			setupURL, err := buildPhoneLink(origin, account, cfg.WingID, key, token.Token, noToken)
			if err != nil {
				return err
			}
			if token.UserID == "" {
				// Keep the auth-check identity with the bearer that proved it,
				// so subsequent link commands work entirely from local state.
				token.UserID = account
				if err := store.Save(token); err != nil {
					return errors.New("cannot save the account identity with the device token")
				}
			}
			warning := "Warning: this setup link contains a credential; keep it private."
			if noToken {
				warning = "Warning: this setup link omits the credential; enter the token separately."
			}
			if _, err := fmt.Fprintln(cmd.ErrOrStderr(), warning); err != nil {
				return errors.New("cannot write setup warning")
			}
			if jsonOutput {
				err = json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					URL string `json:"url"`
				}{setupURL})
			} else {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), setupURL)
			}
			if err != nil {
				return errors.New("cannot write setup link")
			}
			return nil
		},
	}
	link.Flags().BoolVar(&jsonOutput, "json", false, "print the setup URL as JSON")
	link.Flags().BoolVar(&noToken, "no-token", false, "omit the bearer; enter it separately on the phone")
	cmd.AddCommand(link)
	return cmd
}

func checkPhoneOrigin(origin string) error {
	parts, err := url.Parse(origin)
	if err != nil || parts.Scheme != "https" || parts.Hostname() == "" || parts.User != nil ||
		(parts.Path != "" && parts.Path != "/") || parts.RawQuery != "" || parts.ForceQuery || parts.Fragment != "" {
		return errors.New("phone setup requires an exact HTTPS roost origin without a path or credentials")
	}
	return nil
}

func buildPhoneLink(origin, account, wing, key, bearer string, noToken bool) (string, error) {
	if err := checkPhoneOrigin(origin); err != nil {
		return "", err
	}
	publicKey, err := base64.StdEncoding.DecodeString(key)
	if strings.TrimSpace(account) == "" || strings.TrimSpace(wing) == "" || err != nil || len(publicKey) != 32 {
		return "", errors.New("phone setup requires an account, wing ID, and X25519 public key")
	}
	values := url.Values{"origin": {origin}, "account": {account}, "wing": {wing}, "key": {key}}
	if !noToken {
		if strings.TrimSpace(bearer) == "" {
			return "", errors.New("phone setup requires an existing bearer")
		}
		values.Set("token", bearer)
	}
	// Foundation query items treat '+' literally; use %20 for spaces rather
	// than form encoding so every value round-trips through the iOS parser.
	link := "wingthing://home?v=1&" + strings.ReplaceAll(values.Encode(), "+", "%20")
	if len(link) > maxPhoneLinkBytes {
		return "", errors.New("phone setup link is too large")
	}
	return link, nil
}

func fetchPhoneAccount(ctx context.Context, origin, bearer string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/auth/check", nil)
	if err != nil {
		return "", errors.New("cannot build account check")
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse // Never send this credential to another origin.
	}}
	response, err := client.Do(req)
	if err != nil {
		return "", errors.New("cannot check account at the configured roost")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", errors.New("configured roost rejected the account check")
	}
	var info auth.UserInfo
	if err := cmdutil.DecodeCLIAPIResponse(response.Body, &info); err != nil || strings.TrimSpace(info.UserID) == "" {
		return "", errors.New("configured roost returned an invalid account identity")
	}
	return info.UserID, nil
}
