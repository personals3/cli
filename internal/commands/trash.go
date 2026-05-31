package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/personals3/cli/internal/client"
	"github.com/personals3/cli/internal/config"
)

// Trash — list/restore/purge/empty.
//
//   ps3 trash              # list
//   ps3 trash empty        # purge everything (confirm prompt)
//   ps3 trash restore bucket/key [bucket/key ...]
//   ps3 trash purge   bucket/key [bucket/key ...]
func Trash() *cli.Command {
	return &cli.Command{
		Name:  "trash",
		Usage: "Manage soft-deleted objects (list, restore, purge, empty)",
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List items currently in trash",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					cfg, err := config.Load()
					if err != nil {
						return err
					}
					cfg.MustServer()
					c := client.New(cfg)
					var resp struct {
						Items []struct {
							Bucket    string    `json:"bucket"`
							Key       string    `json:"key"`
							Size      int64     `json:"size"`
							DeletedAt time.Time `json:"deletedAt"`
						} `json:"items"`
						TotalBytes int64 `json:"totalBytes"`
					}
					if err := c.Do(ctx, "GET", "/api/trash", nil, &resp); err != nil {
						return err
					}
					for _, it := range resp.Items {
						fmt.Printf("%s/%s\t%s\t%s\n",
							it.Bucket, it.Key, formatBytes(it.Size),
							it.DeletedAt.Format("2006-01-02 15:04"))
					}
					fmt.Fprintf(stderrCounter(), "%d items, %s total\n",
						len(resp.Items), formatBytes(resp.TotalBytes))
					return nil
				},
			},
			{
				Name:      "restore",
				Usage:     "Restore one or more trashed objects",
				ArgsUsage: "<bucket/key> [bucket/key ...]",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return trashAction(ctx, cmd, "POST", "/api/trash", "restored")
				},
			},
			{
				Name:      "purge",
				Usage:     "Permanently delete trashed objects (refunds quota)",
				ArgsUsage: "<bucket/key> [bucket/key ...]",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return trashAction(ctx, cmd, "DELETE", "/api/trash", "purged")
				},
			},
			{
				Name:      "empty",
				Usage:     "Permanently delete every item in trash (optionally only one bucket)",
				ArgsUsage: "[bucket]",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "yes", Usage: "Skip confirmation"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					cfg, err := config.Load()
					if err != nil {
						return err
					}
					cfg.MustServer()
					c := client.New(cfg)

					bucket := cmd.Args().First()
					if !cmd.Bool("yes") {
						scope := "ALL buckets"
						if bucket != "" {
							scope = "bucket " + bucket
						}
						if prompt(fmt.Sprintf("Empty trash for %s? Cannot be undone (y/N): ", scope)) != "y" {
							fmt.Println("aborted")
							return nil
						}
					}

					// Whole-trash path uses the bulk endpoint.
					if bucket == "" {
						var r struct {
							Purged        int   `json:"purged"`
							RefundedBytes int64 `json:"refundedBytes"`
						}
						if err := c.Do(ctx, "DELETE", "/api/trash?all=1", nil, &r); err != nil {
							return err
						}
						fmt.Printf("purged %d items, reclaimed %s\n",
							r.Purged, formatBytes(r.RefundedBytes))
						return nil
					}

					// Per-bucket: there's no bulk-by-bucket endpoint, so list
					// then purge each. The trash list is unscoped, so filter
					// client-side.
					var list struct {
						Items []struct {
							Bucket string `json:"bucket"`
							Key    string `json:"key"`
							Size   int64  `json:"size"`
						} `json:"items"`
					}
					if err := c.Do(ctx, "GET", "/api/trash", nil, &list); err != nil {
						return err
					}
					items := []map[string]string{}
					var totalBytes int64
					for _, it := range list.Items {
						if it.Bucket != bucket {
							continue
						}
						items = append(items, map[string]string{
							"bucket": it.Bucket, "key": it.Key,
						})
						totalBytes += it.Size
					}
					if len(items) == 0 {
						fmt.Printf("trash is empty for %s\n", bucket)
						return nil
					}
					body := map[string]any{"items": items}
					if err := c.Do(ctx, "DELETE", "/api/trash", body, nil); err != nil {
						return err
					}
					fmt.Printf("purged %d items from %s, reclaimed %s\n",
						len(items), bucket, formatBytes(totalBytes))
					return nil
				},
			},
		},
	}
}

func trashAction(ctx context.Context, cmd *cli.Command, method, path, verb string) error {
	if cmd.Args().Len() == 0 {
		return fmt.Errorf("%s requires at least one bucket/key", verb)
	}
	items := []map[string]string{}
	for _, t := range cmd.Args().Slice() {
		bucket, key := splitBucketKey(t)
		if bucket == "" || key == "" {
			return fmt.Errorf("invalid target %q", t)
		}
		items = append(items, map[string]string{"bucket": bucket, "key": key})
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.MustServer()
	c := client.New(cfg)
	body := map[string]any{"items": items}
	var resp map[string]any
	if err := c.Do(ctx, method, path, body, &resp); err != nil {
		return err
	}
	fmt.Printf("%s %d items\n", verb, getInt(resp, "purged", getInt(resp, "restored", len(items))))
	return nil
}

func getInt(m map[string]any, k string, def int) int {
	if v, ok := m[k]; ok {
		if n, ok := v.(float64); ok {
			return int(n)
		}
	}
	return def
}
