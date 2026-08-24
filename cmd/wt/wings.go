package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"github.com/spf13/cobra"
)

type wingFinderEntry struct {
	WingID        string           `json:"wing_id"`
	Online        bool             `json:"online"`
	Owner         string           `json:"owner,omitempty"`
	UserID        string           `json:"user_id,omitempty"`
	OrgID         string           `json:"org_id,omitempty"`
	RemoteNode    string           `json:"remote_node,omitempty"`
	LatestVersion string           `json:"latest_version,omitempty"`
	Hostname      string           `json:"hostname,omitempty"`
	Label         string           `json:"label,omitempty"`
	Platform      string           `json:"platform,omitempty"`
	Version       string           `json:"version,omitempty"`
	Agents        []string         `json:"agents,omitempty"`
	Projects      []ws.WingProject `json:"projects,omitempty"`
	Locked        bool             `json:"locked"`
	Spectate      bool             `json:"spectate"`
	Verified      bool             `json:"verified"`
	ProbeError    string           `json:"probe_error,omitempty"`
}

type wingFinderProbe struct {
	Hostname string           `json:"hostname"`
	Label    string           `json:"wing_label"`
	Platform string           `json:"platform"`
	Version  string           `json:"version"`
	Agents   []string         `json:"agents"`
	Projects []ws.WingProject `json:"projects"`
	Locked   bool             `json:"locked"`
	Spectate bool             `json:"spectate"`
}

func wingsCmd() *cobra.Command {
	var roostFlag string
	var jsonFlag bool
	var noProbeFlag bool
	var timeoutFlag time.Duration

	cmd := &cobra.Command{
		Use:     "wings",
		Aliases: []string{"find"},
		Short:   "Find online wings available through a roost",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			tokenStore := auth.NewTokenStore(cfg.Dir)
			token, err := tokenStore.Load()
			if err != nil {
				return err
			}
			if !tokenStore.IsValid(token) {
				return fmt.Errorf("not logged in — run: wt login --roost <url>")
			}

			relayURL := finderRelayURL(cfg, roostFlag)
			client := &ws.TunnelClient{RelayURL: relayURL, DeviceToken: token.Token}
			wings, err := client.ListWings(cmd.Context())
			if err != nil {
				return err
			}

			entries := make([]wingFinderEntry, len(wings))
			for i, wing := range wings {
				entries[i] = wingFinderEntry{
					WingID: wing.WingID, Online: true, Owner: wing.Owner,
					UserID: wing.UserID, OrgID: wing.OrgID, RemoteNode: wing.RemoteNode,
					LatestVersion: wing.LatestVersion,
				}
			}

			if !noProbeFlag && len(wings) > 0 {
				privateKey, keyErr := auth.LoadPrivateKey(cfg.Dir)
				if keyErr != nil {
					return fmt.Errorf("load native client key: %w", keyErr)
				}
				client.PrivKey = privateKey
				client.KnownWingsPath = filepath.Join(cfg.Dir, "known_wings.json")

				// Identity pin updates are serialized because first-use discovery may
				// add several entries to the same native pin store.
				for i, wing := range wings {
					if pinErr := client.VerifyWingIdentity(wing); pinErr != nil {
						entries[i].ProbeError = pinErr.Error()
					}
				}

				for i, wing := range wings {
					if entries[i].ProbeError != "" {
						continue
					}
					probeCtx, cancel := context.WithTimeout(cmd.Context(), timeoutFlag)
					var probe wingFinderProbe
					probeErr := client.Stream(probeCtx, wing.WingID, wing.PublicKey, map[string]string{
						"type": "wing.info",
					}, func(data []byte) error {
						return json.Unmarshal(data, &probe)
					})
					cancel()
					if probeErr != nil {
						entries[i].ProbeError = probeErr.Error()
						continue
					}
					entries[i].Verified = true
					entries[i].Hostname = probe.Hostname
					entries[i].Label = probe.Label
					entries[i].Platform = probe.Platform
					entries[i].Version = probe.Version
					entries[i].Agents = probe.Agents
					entries[i].Projects = probe.Projects
					entries[i].Locked = probe.Locked
					entries[i].Spectate = probe.Spectate
				}
			}

			sort.Slice(entries, func(i, j int) bool {
				left := entries[i].Label + entries[i].Hostname + entries[i].WingID
				right := entries[j].Label + entries[j].Hostname + entries[j].WingID
				return left < right
			})
			if jsonFlag {
				return writeSessionJSON(entries)
			}
			printWingFinderEntries(entries, noProbeFlag)
			return nil
		},
	}

	cmd.Flags().StringVar(&roostFlag, "roost", "", "roost URL (default: config or wingthing.ai)")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	cmd.Flags().BoolVar(&noProbeFlag, "no-probe", false, "list the relay roster without encrypted wing.info probes")
	cmd.Flags().DurationVar(&timeoutFlag, "timeout", 5*time.Second, "timeout for each encrypted wing probe")
	return cmd
}

func finderRelayURL(cfg *config.Config, override string) string {
	if strings.TrimSpace(override) == "" {
		return resolveRelayHTTPURL(cfg)
	}
	copy := *cfg
	copy.RoostURL = override
	return resolveRelayHTTPURL(&copy)
}

func printWingFinderEntries(entries []wingFinderEntry, noProbe bool) {
	if len(entries) == 0 {
		fmt.Println("no wings online")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "WING\tNAME\tOWNER\tPLATFORM\tVERSION\tSTATUS")
	for _, entry := range entries {
		name := entry.Label
		if name == "" {
			name = entry.Hostname
		}
		if name == "" {
			name = "-"
		}
		owner := entry.Owner
		if owner == "" {
			owner = entry.UserID
		}
		if owner == "" {
			owner = "-"
		}
		platform := entry.Platform
		if platform == "" {
			platform = "-"
		}
		version := entry.Version
		if version == "" {
			version = "-"
		}
		status := "online"
		if entry.Verified {
			status = "verified"
		} else if entry.ProbeError != "" {
			status = "online; probe failed: " + entry.ProbeError
		} else if !noProbe {
			status = "online; unverified"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", entry.WingID, name, owner, platform, version, status)
	}
	w.Flush()
}
