package commands

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/personals3/cli/internal/client"
	"github.com/personals3/cli/internal/config"
)

// Auth groups account-level commands. Currently only `keys`; login/logout/whoami
// stay at the top level because they're the most common entry points and we
// don't want to break existing muscle memory by moving them.
//
//   ps3 auth keys list
//   ps3 auth keys create --name "laptop" --expires 90d
//   ps3 auth keys revoke <id>
func Auth() *cli.Command {
	return &cli.Command{
		Name:  "auth",
		Usage: "Account management (API keys, etc.)",
		Commands: []*cli.Command{
			authKeys(),
		},
	}
}

func authKeys() *cli.Command {
	return &cli.Command{
		Name:  "keys",
		Usage: "Manage long-lived API keys (for scripts/CI)",
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List API keys for the current user",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					cfg, err := config.Load()
					if err != nil {
						return err
					}
					cfg.MustServer()
					c := client.New(cfg)
					var resp struct {
						Keys []struct {
							ID         string     `json:"id"`
							Name       string     `json:"name"`
							KeyPrefix  string     `json:"keyPrefix"`
							LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
							ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
							CreatedAt  time.Time  `json:"createdAt"`
						} `json:"keys"`
					}
					if err := c.Do(ctx, "GET", "/api/auth/keys", nil, &resp); err != nil {
						return err
					}
					if len(resp.Keys) == 0 {
						fmt.Fprintln(stderrCounter(), "no API keys")
						return nil
					}
					// tabwriter keeps columns aligned without a third-party dep.
					tw := tabwriter.NewWriter(stderrCounter(), 0, 2, 2, ' ', 0)
					fmt.Fprintln(tw, "ID\tNAME\tPREFIX\tEXPIRES\tLAST USED")
					fmt.Fprintln(tw, "--\t----\t------\t-------\t---------")
					_ = tw.Flush()
					// Data goes to stdout for piping; only headers go to stderr.
					tw = tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
					for _, k := range resp.Keys {
						fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
							k.ID,
							dashIfEmpty(k.Name),
							k.KeyPrefix,
							fmtKeyTime(k.ExpiresAt, "never"),
							fmtKeyTime(k.LastUsedAt, "never"))
					}
					return tw.Flush()
				},
			},
			{
				Name:  "create",
				Usage: "Create a new API key (plaintext printed ONCE)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "name", Usage: "Human label (e.g. \"ci\", \"laptop\")"},
					&cli.StringFlag{Name: "expires",
						Usage: "Lifetime (e.g. 30d, 90d, 1y). Omit for no expiry. " + DurationHelp},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					name := cmd.String("name")
					if name == "" {
						return fmt.Errorf("--name required")
					}
					body := map[string]any{"name": name}
					if s := cmd.String("expires"); s != "" {
						d, err := parseDuration(s)
						if err != nil {
							return err
						}
						if d <= 0 {
							return fmt.Errorf("--expires must be positive")
						}
						body["expiresAt"] = time.Now().Add(d).UTC().Format(time.RFC3339)
					}
					cfg, err := config.Load()
					if err != nil {
						return err
					}
					cfg.MustServer()
					c := client.New(cfg)
					var resp struct {
						ID        string     `json:"id"`
						Name      string     `json:"name"`
						Key       string     `json:"key"` // plaintext, shown once
						KeyPrefix string     `json:"keyPrefix"`
						ExpiresAt *time.Time `json:"expiresAt,omitempty"`
					}
					if err := c.Do(ctx, "POST", "/api/auth/keys", body, &resp); err != nil {
						return err
					}
					// Loud warning to stderr, plaintext to stdout for clean piping.
					fmt.Fprintln(stderrCounter(),
						"!! Copy this key NOW — the server only shows it once. !!")
					fmt.Fprintf(stderrCounter(), "   id=%s  prefix=%s",
						resp.ID, resp.KeyPrefix)
					if resp.ExpiresAt != nil {
						fmt.Fprintf(stderrCounter(), "  expires=%s",
							humanizeUntil(*resp.ExpiresAt))
					}
					fmt.Fprintln(stderrCounter())
					fmt.Println(resp.Key)
					return nil
				},
			},
			{
				Name:      "revoke",
				Usage:     "Revoke (delete) an API key by ID",
				ArgsUsage: "<id>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					id := cmd.Args().First()
					if id == "" {
						return fmt.Errorf("key id required")
					}
					cfg, err := config.Load()
					if err != nil {
						return err
					}
					cfg.MustServer()
					c := client.New(cfg)
					if err := c.Do(ctx, "DELETE", "/api/auth/keys/"+id, nil, nil); err != nil {
						return err
					}
					fmt.Printf("revoked %s\n", id)
					return nil
				},
			},
		},
	}
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func fmtKeyTime(t *time.Time, zero string) string {
	if t == nil || t.IsZero() {
		return zero
	}
	return t.Local().Format("2006-01-02")
}
