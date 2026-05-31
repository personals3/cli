package commands

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/personals3/cli/internal/client"
	"github.com/personals3/cli/internal/config"
)

// Rm deletes objects. Default = soft-delete (trash). With --purge, skips trash.
//
//   ps3 rm my-bucket/file.txt              # trash
//   ps3 rm --purge my-bucket/file.txt      # permanent
//   ps3 rm -r my-bucket/photos/            # bulk-trash everything under a prefix
func Rm() *cli.Command {
	return &cli.Command{
		Name:      "rm",
		Usage:     "Delete objects (soft-delete to trash by default)",
		ArgsUsage: "<bucket/key> [bucket/key ...]",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "purge", Usage: "Skip trash; permanently delete + refund quota"},
			&cli.BoolFlag{Name: "recursive", Aliases: []string{"r"},
				Usage: "Treat the target as a prefix; delete every object under it"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() == 0 {
				return fmt.Errorf("rm requires at least one bucket/key")
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			cfg.MustServer()
			c := client.New(cfg)
			purge := cmd.Bool("purge")
			recursive := cmd.Bool("recursive")

			for _, target := range cmd.Args().Slice() {
				bucket, key := splitBucketKey(target)
				if bucket == "" || key == "" {
					return fmt.Errorf("invalid target %q (need bucket/key)", target)
				}
				if recursive {
					if err := rmRecursive(ctx, c, bucket, key, purge); err != nil {
						return err
					}
					continue
				}
				if err := rmOne(ctx, c, bucket, key, purge); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func rmOne(ctx context.Context, c *client.Client, bucket, key string, purge bool) error {
	u := "/api/" + url.PathEscape(bucket) + "/" + client.EncodeKey(key)
	if purge {
		u += "?purge"
	}
	if err := c.Do(ctx, "DELETE", u, nil, nil); err != nil {
		return fmt.Errorf("delete %s/%s: %w", bucket, key, err)
	}
	verb := "trashed"
	if purge {
		verb = "purged"
	}
	fmt.Printf("%s %s/%s\n", verb, bucket, key)
	return nil
}

func rmRecursive(ctx context.Context, c *client.Client, bucket, prefix string, purge bool) error {
	q := url.Values{}
	q.Set("prefix", prefix)
	q.Set("max-keys", "1000")
	var resp struct {
		Objects []struct {
			Key string `json:"key"`
		} `json:"objects"`
		Truncated bool `json:"truncated"`
	}
	if err := c.Do(ctx, "GET",
		"/api/"+url.PathEscape(bucket)+"?"+q.Encode(), nil, &resp,
	); err != nil {
		return err
	}
	if len(resp.Objects) == 0 {
		fmt.Printf("no objects under %s/%s\n", bucket, prefix)
		return nil
	}
	keys := make([]string, 0, len(resp.Objects))
	for _, o := range resp.Objects {
		keys = append(keys, o.Key)
	}
	if purge {
		// Bulk-purge: iterate ?purge per object (no bulk endpoint).
		for _, k := range keys {
			if err := rmOne(ctx, c, bucket, k, true); err != nil {
				return err
			}
		}
	} else {
		body := map[string]any{"keys": keys}
		var r struct {
			Deleted int `json:"deleted"`
		}
		if err := c.Do(ctx, "POST",
			"/api/"+url.PathEscape(bucket)+"?delete", body, &r,
		); err != nil {
			return err
		}
		fmt.Printf("trashed %d objects under %s/%s\n", r.Deleted, bucket, prefix)
	}
	if resp.Truncated {
		fmt.Println("note: more than 1000 objects matched; re-run to delete the rest")
	}
	return nil
}

// unused but kept to avoid import-cycle warnings if we later cross-reference
var _ = strings.TrimSpace
