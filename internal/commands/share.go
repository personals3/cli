package commands

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/personals3/cli/internal/client"
	"github.com/personals3/cli/internal/config"
)

// Share generates a presigned URL for a remote object, or lists/revokes
// previously-issued URLs.
//
//   ps3 share my-bucket/cat.jpg                      # GET, 24h default
//   ps3 share --expires 1h my-bucket/cat.jpg
//   ps3 share --upload  my-bucket/dropbox/upload.bin # PUT URL — anyone can write
//   ps3 share --download my-bucket/cat.jpg           # force download (Content-Disposition)
//
//   ps3 share list                                   # list active share links
//   ps3 share list --all                             # include revoked + expired
//   ps3 share revoke <share-id>                      # revoke one
//   ps3 share revoke-all                             # revoke every active share
//   ps3 share extend <share-id> --by 24h             # extend expiry
func Share() *cli.Command {
	return &cli.Command{
		Name:      "share",
		Usage:     "Create or manage presigned URLs",
		ArgsUsage: "<bucket/key>",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "expires", Value: "24h",
				Usage: "Validity (server caps at 30d). " + DurationHelp},
			&cli.BoolFlag{Name: "download",
				Usage: "Force browser download (Content-Disposition: attachment)"},
			&cli.BoolFlag{Name: "upload",
				Usage: "Generate a PUT URL (writeable) instead of a GET URL — " +
					"anyone with the URL can overwrite this exact key"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			target := cmd.Args().First()
			if target == "" {
				return fmt.Errorf("share requires bucket/key")
			}
			bucket, key := splitBucketKey(target)
			if bucket == "" || key == "" {
				return fmt.Errorf("invalid target %q", target)
			}

			d, err := time.ParseDuration(cmd.String("expires"))
			if err != nil {
				// Allow shorthand like "7d", "30d"
				if strings.HasSuffix(cmd.String("expires"), "d") {
					var days int
					if _, err := fmt.Sscanf(cmd.String("expires"), "%dd", &days); err != nil {
						return fmt.Errorf("invalid --expires: %w", err)
					}
					d = time.Duration(days) * 24 * time.Hour
				} else {
					return fmt.Errorf("invalid --expires: %w", err)
				}
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			cfg.MustServer()
			c := client.New(cfg)

			method := "GET"
			if cmd.Bool("upload") {
				method = "PUT"
			}
			body := map[string]any{
				"expiresSec": int(d.Seconds()),
				"method":     method,
				"download":   cmd.Bool("download"),
			}

			u := "/api/" + url.PathEscape(bucket) + "/" + client.EncodeKey(key) + "?presign"
			var resp struct {
				URL       string `json:"url"`
				ExpiresAt int64  `json:"expiresAt"`
				Method    string `json:"method"`
				ShareID   string `json:"shareId"`
			}
			if err := c.Do(ctx, "POST", u, body, &resp); err != nil {
				return err
			}

			full := cfg.Server + resp.URL
			fmt.Println(full)
			fmt.Fprintf(stderrCounter(),
				"method=%s  expires=%s  id=%s\n",
				resp.Method,
				humanizeUntil(time.Unix(resp.ExpiresAt, 0)),
				resp.ShareID)
			return nil
		},
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List share links",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "all", Usage: "Include revoked + expired"},
					&cli.StringFlag{Name: "bucket", Usage: "Filter to one bucket"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					cfg, err := config.Load()
					if err != nil {
						return err
					}
					cfg.MustServer()
					c := client.New(cfg)
					qp := url.Values{}
					if !cmd.Bool("all") {
						qp.Set("active", "1")
					}
					if b := cmd.String("bucket"); b != "" {
						qp.Set("bucket", b)
					}
					var resp struct {
						Shares []struct {
							ID        string    `json:"id"`
							Bucket    string    `json:"bucket"`
							Key       string    `json:"key"`
							Method    string    `json:"method"`
							ExpiresAt time.Time `json:"expiresAt"`
							Revoked   bool      `json:"revoked"`
							Expired   bool      `json:"expired"`
							Active    bool      `json:"active"`
							UseCount  int       `json:"useCount"`
						} `json:"shares"`
					}
					if err := c.Do(ctx, "GET", "/api/shares?"+qp.Encode(), nil, &resp); err != nil {
						return err
					}
					for _, s := range resp.Shares {
						state := "active"
						if s.Revoked {
							state = "revoked"
						} else if s.Expired {
							state = "expired"
						}
						fmt.Printf("%s\t%s\t%s/%s\t%s\thits=%d\texp=%s\n",
							s.ID, state, s.Bucket, s.Key, s.Method,
							s.UseCount, humanizeUntil(s.ExpiresAt))
					}
					return nil
				},
			},
			{
				Name:      "revoke",
				Usage:     "Revoke a share link by ID",
				ArgsUsage: "<share-id>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					id := cmd.Args().First()
					if id == "" {
						return fmt.Errorf("share-id required")
					}
					cfg, err := config.Load()
					if err != nil {
						return err
					}
					cfg.MustServer()
					c := client.New(cfg)
					if err := c.Do(ctx, "DELETE", "/api/shares/"+id, nil, nil); err != nil {
						return err
					}
					fmt.Printf("revoked %s\n", id)
					return nil
				},
			},
			{
				Name:  "revoke-all",
				Usage: "Revoke every active share link",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "yes", Usage: "Skip confirmation"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					if !cmd.Bool("yes") {
						if prompt("Revoke ALL active share links? (y/N): ") != "y" {
							fmt.Println("aborted")
							return nil
						}
					}
					cfg, err := config.Load()
					if err != nil {
						return err
					}
					cfg.MustServer()
					c := client.New(cfg)
					var resp struct {
						Revoked int `json:"revoked"`
					}
					if err := c.Do(ctx, "DELETE", "/api/shares?all=1", nil, &resp); err != nil {
						return err
					}
					fmt.Printf("revoked %d share(s)\n", resp.Revoked)
					return nil
				},
			},
			{
				Name:      "extend",
				Usage:     "Extend (or shorten) a share's expiry — new expiry is " +
					"<now + value>, capped at +30d from now",
				ArgsUsage: "<share-id>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "by",
						Usage: "New validity from now. " + DurationHelp},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					id := cmd.Args().First()
					if id == "" {
						return fmt.Errorf("share-id required")
					}
					byStr := cmd.String("by")
					if byStr == "" {
						return fmt.Errorf("--by required (e.g. --by 24h)")
					}
					d, err := parseDuration(byStr)
					if err != nil {
						return err
					}
					cfg, err := config.Load()
					if err != nil {
						return err
					}
					cfg.MustServer()
					c := client.New(cfg)
					// PATCH with expiresInSec relative to NOW. Server caps at 30d.
					body := map[string]any{"expiresInSec": int(d.Seconds())}
					var resp struct {
						ExpiresAt int64 `json:"expiresAt"`
					}
					if err := c.Do(ctx, "PATCH", "/api/shares/"+id, body, &resp); err != nil {
						return err
					}
					fmt.Printf("new expiry: %s\n",
						humanizeUntil(time.Unix(resp.ExpiresAt, 0)))
					return nil
				},
			},
		},
	}
}

// DurationHelp is a one-liner that documents every unit parseDuration accepts.
// Reuse it in every CLI flag's Usage string so users can see it from `--help`.
const DurationHelp = "Units: ns, us(µs), ms, s, m, h, d, w, mo (=30d), y (=365d). " +
	"Mix Go-standard units freely (e.g. 1h30m). Single unit only for d/w/mo/y. " +
	"Negative values are allowed (e.g. -30m shortens)."

// parseDuration accepts:
//
//	Go-standard units (any combination):   ns us µs ms s m h     → time.ParseDuration
//	Single non-standard unit:               d (24h), w (7d), mo (30d), y (365d)
//
// Examples that parse:
//
//	"45s"      → 45 seconds
//	"5m"       → 5 minutes
//	"2h30m"    → 2 hours 30 minutes
//	"1d"       → 24 hours
//	"7d"       → 7 days
//	"2w"       → 14 days
//	"1mo"      → 30 days (approximation — calendar months vary 28..31)
//	"1y"       → 365 days (approximation — leap years are 366)
//	"-1h"      → minus one hour (shortens, in share extend context)
//
// Does NOT parse (yet):
//
//	"1d12h"    → mixed d + Go units. Use "36h" instead.
//	"1mo15d"   → mixed mo + d.
//
// "mo" is checked before "m" so "1mo" doesn't get treated as "1 minute followed by o".
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	// Negative? Strip sign, recurse, then negate.
	if strings.HasPrefix(s, "-") {
		d, err := parseDuration(strings.TrimPrefix(s, "-"))
		if err != nil {
			return 0, err
		}
		return -d, nil
	}

	// Try Go-native first (covers s/m/h and combos like 2h30m).
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}

	// Order matters — "mo" must be checked before "m".
	type unit struct {
		suffix string
		mult   time.Duration
	}
	units := []unit{
		{"mo", 30 * 24 * time.Hour},
		{"y", 365 * 24 * time.Hour},
		{"w", 7 * 24 * time.Hour},
		{"d", 24 * time.Hour},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			numStr := strings.TrimSuffix(s, u.suffix)
			var n int
			if _, err := fmt.Sscanf(numStr, "%d", &n); err != nil || numStr == "" {
				return 0, fmt.Errorf("bad duration %q (digits then %q, e.g. 7%s)",
					s, u.suffix, u.suffix)
			}
			return time.Duration(n) * u.mult, nil
		}
	}
	return 0, fmt.Errorf("bad duration %q — %s", s, DurationHelp)
}
