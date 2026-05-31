package commands

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/urfave/cli/v3"
	"golang.org/x/term"

	"github.com/personals3/cli/internal/client"
	"github.com/personals3/cli/internal/config"
)

// Login authenticates against a server and persists the credential.
//
// Three modes:
//
//  1. Bare invocation — if a valid token is already saved, prints
//     "already logged in" and exits 0 (like `gh auth login`, `aws configure`).
//     Pass --force to re-authenticate.
//
//  2. Password mode (default for first-time use):
//     prompts for email + password. If the account has 2FA enabled, the
//     server returns a challenge; we prompt for the 6-digit code and
//     exchange (challenge, code) for the JWT.
//
//  3. API-key mode (--token <key>): skip password + 2FA entirely. The key
//     is generated in the dashboard (API Keys page) and acts like a GitHub
//     personal access token — long-lived, per-device, individually revocable.
//     Recommended for CI, scripts, and any "headless" use.
func Login() *cli.Command {
	return &cli.Command{
		Name:  "login",
		Usage: "Log in (or confirm you're logged in) and save credentials",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "server",
				Usage: "Server base URL (e.g. http://localhost:8080)"},
			&cli.StringFlag{Name: "email"},
			&cli.StringFlag{Name: "password",
				Usage: "Plain password (avoid; prefer the interactive prompt)"},
			&cli.StringFlag{Name: "token",
				Usage: "Use an existing API key (from dashboard → API Keys) " +
					"instead of email/password. Skips 2FA entirely. " +
					"Best for scripts and shared machines."},
			&cli.BoolFlag{Name: "force",
				Usage: "Re-authenticate even if a valid session exists"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if s := cmd.String("server"); s != "" {
				cfg.Server = strings.TrimSuffix(s, "/")
			}

			// Mode 1: already logged in? Confirm by calling /auth/me.
			if !cmd.Bool("force") && cfg.Server != "" && cfg.Token != "" &&
				cmd.String("token") == "" && cmd.String("email") == "" {
				c := client.New(cfg)
				var me struct {
					Email string `json:"email"`
					Role  string `json:"role"`
				}
				if err := c.Do(ctx, "GET", "/api/auth/me", nil, &me); err == nil {
					fmt.Printf("already logged in as %s (%s) @ %s\n",
						me.Email, me.Role, cfg.Server)
					fmt.Fprintln(stderrCounter(),
						"use `ps3 login --force` to re-authenticate, or `ps3 logout` first")
					return nil
				}
				// fall through: token is stale or server unreachable — re-login
			}

			if cfg.Server == "" {
				return fmt.Errorf("server URL required (use --server)")
			}

			// Mode 3: API key — skip password + 2FA entirely.
			if tok := cmd.String("token"); tok != "" {
				cfg.Token = tok
				// Confirm the token works so we don't save a broken one.
				c := client.New(cfg)
				var me struct {
					Email string `json:"email"`
					Role  string `json:"role"`
				}
				if err := c.Do(ctx, "GET", "/api/auth/me", nil, &me); err != nil {
					return fmt.Errorf("token rejected by server: %w", err)
				}
				cfg.Email = me.Email
				if err := config.Save(cfg); err != nil {
					return err
				}
				fmt.Printf("logged in as %s (%s) @ %s (via API key)\n",
					me.Email, me.Role, cfg.Server)
				return nil
			}

			// Mode 2: email + password (+ 2FA if enabled).
			email := cmd.String("email")
			if email == "" {
				email = prompt("email: ")
			}
			pass := cmd.String("password")
			if pass == "" {
				pass = promptSecret("password: ")
			}

			c := client.New(cfg)
			tok, err := c.Login(ctx, email, pass)
			if err != nil {
				return err
			}

			// If the server returned a 2FA challenge instead of a token,
			// prompt for the code.
			if strings.HasPrefix(tok, "2fa:") {
				challenge := strings.TrimPrefix(tok, "2fa:")
				code := prompt("2FA code (6 digits or recovery): ")
				finalTok, err := c.Verify2FA(ctx, challenge, code)
				if err != nil {
					return err
				}
				tok = finalTok
			}
			cfg.Token = tok
			cfg.Email = email
			if err := config.Save(cfg); err != nil {
				return err
			}
			fmt.Printf("logged in as %s @ %s\n", email, cfg.Server)
			return nil
		},
	}
}

// Logout wipes the local token (server-side stays valid until expiry).
func Logout() *cli.Command {
	return &cli.Command{
		Name:  "logout",
		Usage: "Clear locally stored credentials",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			cfg.Token = ""
			if err := config.Save(cfg); err != nil {
				return err
			}
			fmt.Println("logged out")
			return nil
		},
	}
}

// Whoami prints the authenticated user's info.
func Whoami() *cli.Command {
	return &cli.Command{
		Name:  "whoami",
		Usage: "Show the currently authenticated user",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			cfg.MustServer()
			c := client.New(cfg)
			var me struct {
				Email      string `json:"email"`
				Role       string `json:"role"`
				UsedBytes  int64  `json:"usedBytes"`
				QuotaBytes int64  `json:"quotaBytes"`
			}
			if err := c.Do(ctx, "GET", "/api/auth/me", nil, &me); err != nil {
				return err
			}
			fmt.Printf("%s (%s) — used %s / quota %s\n",
				me.Email, me.Role, formatBytes(me.UsedBytes), formatBytes(me.QuotaBytes))
			fmt.Printf("server: %s\n", cfg.Server)
			if cfg.DefaultBucket != "" {
				fmt.Printf("default bucket: %s\n", cfg.DefaultBucket)
			}
			return nil
		},
	}
}

// ---------- prompt helpers ----------

func prompt(label string) string {
	fmt.Fprint(os.Stderr, label)
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}

func promptSecret(label string) string {
	fmt.Fprint(os.Stderr, label)
	b, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		// Fall back to visible input if not a TTY (CI / piped)
		r := bufio.NewReader(os.Stdin)
		line, _ := r.ReadString('\n')
		return strings.TrimSpace(line)
	}
	return strings.TrimSpace(string(b))
}
