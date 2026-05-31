package commands

import (
	"context"
	"fmt"
	"net/url"

	"github.com/urfave/cli/v3"

	"github.com/personals3/cli/internal/client"
	"github.com/personals3/cli/internal/config"
)

// Bucket — list/create/delete/patch bucket settings.
//
//   ps3 bucket list
//   ps3 bucket create my-bucket --mode media
//   ps3 bucket delete my-bucket [--force]
//   ps3 bucket patch  my-bucket --public --versioning --archived
func Bucket() *cli.Command {
	return &cli.Command{
		Name:  "bucket",
		Usage: "Manage buckets",
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List buckets owned by the current user",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return Ls().Action(ctx, cmd)
				},
			},
			{
				Name:      "create",
				Usage:     "Create a new bucket",
				ArgsUsage: "<name>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "mode", Value: "none",
						Usage: "auto-transcode mode: none | media | photos_only"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					name := cmd.Args().First()
					if name == "" {
						return fmt.Errorf("bucket name required")
					}
					cfg, err := config.Load()
					if err != nil {
						return err
					}
					cfg.MustServer()
					c := client.New(cfg)
					body := map[string]any{"autoTranscodeMode": cmd.String("mode")}
					if err := c.Do(ctx, "PUT", "/api/"+url.PathEscape(name), body, nil); err != nil {
						return err
					}
					fmt.Printf("created bucket %s (mode=%s)\n", name, cmd.String("mode"))
					return nil
				},
			},
			{
				Name:      "delete",
				Usage:     "Delete a bucket",
				ArgsUsage: "<name>",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "force", Usage: "Delete even if not empty (wipes objects + segments + refunds quota)"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					name := cmd.Args().First()
					if name == "" {
						return fmt.Errorf("bucket name required")
					}
					cfg, err := config.Load()
					if err != nil {
						return err
					}
					cfg.MustServer()
					c := client.New(cfg)
					u := "/api/" + url.PathEscape(name)
					if cmd.Bool("force") {
						u += "?force=1"
					}
					if err := c.Do(ctx, "DELETE", u, nil, nil); err != nil {
						return err
					}
					fmt.Printf("deleted bucket %s\n", name)
					return nil
				},
			},
			{
				Name:      "patch",
				Usage:     "Update bucket settings (public, versioning, archived, mode)",
				ArgsUsage: "<name>",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "public"},
					&cli.BoolFlag{Name: "private", Usage: "Inverse of --public"},
					&cli.BoolFlag{Name: "versioning"},
					&cli.BoolFlag{Name: "no-versioning"},
					&cli.BoolFlag{Name: "archived"},
					&cli.BoolFlag{Name: "unarchived"},
					&cli.StringFlag{Name: "mode", Usage: "none | media | photos_only"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					name := cmd.Args().First()
					if name == "" {
						return fmt.Errorf("bucket name required")
					}
					body := map[string]any{}
					if cmd.Bool("public")        { body["isPublic"] = true  }
					if cmd.Bool("private")       { body["isPublic"] = false }
					if cmd.Bool("versioning")    { body["versioning"] = true  }
					if cmd.Bool("no-versioning") { body["versioning"] = false }
					if cmd.Bool("archived")      { body["archived"] = true  }
					if cmd.Bool("unarchived")    { body["archived"] = false }
					if s := cmd.String("mode"); s != "" { body["autoTranscodeMode"] = s }
					if len(body) == 0 {
						return fmt.Errorf("no changes specified")
					}
					cfg, err := config.Load()
					if err != nil {
						return err
					}
					cfg.MustServer()
					c := client.New(cfg)
					if err := c.Do(ctx, "PATCH", "/api/"+url.PathEscape(name), body, nil); err != nil {
						return err
					}
					fmt.Printf("updated bucket %s: %v\n", name, body)
					return nil
				},
			},
		},
	}
}
