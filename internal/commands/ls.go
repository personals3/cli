package commands

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/personals3/cli/internal/client"
	"github.com/personals3/cli/internal/config"
)

// Ls — list buckets, or list objects under a path.
//   ps3 ls                       → list buckets
//   ps3 ls my-bucket             → top-level of bucket
//   ps3 ls my-bucket/photos/     → contents of photos/ folder
//   ps3 ls my-bucket --recursive → flat listing of every object
func Ls() *cli.Command {
	return &cli.Command{
		Name:      "ls",
		Usage:     "List buckets or objects",
		ArgsUsage: "[bucket[/prefix/]]",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "recursive", Aliases: []string{"r"},
				Usage: "Show every object under the prefix (no folder grouping)"},
			&cli.BoolFlag{Name: "long", Aliases: []string{"l"},
				Usage: "Long format: size + last modified"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			cfg.MustServer()
			c := client.New(cfg)

			target := cmd.Args().First()
			if target == "" {
				return listBuckets(ctx, c, cmd.Bool("long"))
			}

			bucket, prefix := splitBucketKey(target)
			return listObjects(ctx, c, bucket, prefix,
				cmd.Bool("recursive"), cmd.Bool("long"))
		},
	}
}

func listBuckets(ctx context.Context, c *client.Client, long bool) error {
	var resp struct {
		Buckets []struct {
			Name              string    `json:"name"`
			CreatedAt         time.Time `json:"createdAt"`
			AutoTranscodeMode string    `json:"autoTranscodeMode"`
			IsPublic          bool      `json:"isPublic"`
			Versioning        bool      `json:"versioning"`
			Archived          bool      `json:"archived"`
		} `json:"buckets"`
	}
	if err := c.Do(ctx, "GET", "/api/", nil, &resp); err != nil {
		return err
	}
	for _, b := range resp.Buckets {
		if !long {
			fmt.Println(b.Name)
			continue
		}
		flags := []string{}
		if b.IsPublic   { flags = append(flags, "public") }
		if b.Versioning { flags = append(flags, "versioned") }
		if b.Archived   { flags = append(flags, "archived") }
		if b.AutoTranscodeMode != "none" && b.AutoTranscodeMode != "" {
			flags = append(flags, b.AutoTranscodeMode)
		}
		fmt.Printf("%-30s  %s  %s\n",
			b.Name, b.CreatedAt.Format("2006-01-02"), strings.Join(flags, ","))
	}
	return nil
}

func listObjects(ctx context.Context, c *client.Client, bucket, prefix string,
	recursive, long bool,
) error {
	q := url.Values{}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if !recursive {
		q.Set("delimiter", "/")
	}
	q.Set("max-keys", "1000")

	var resp struct {
		Objects []struct {
			Key          string    `json:"key"`
			Size         int64     `json:"size"`
			LastModified time.Time `json:"lastModified"`
		} `json:"objects"`
		CommonPrefixes []string `json:"commonPrefixes"`
		Truncated      bool     `json:"truncated"`
	}
	if err := c.Do(ctx, "GET", "/api/"+url.PathEscape(bucket)+"?"+q.Encode(), nil, &resp); err != nil {
		return err
	}

	for _, p := range resp.CommonPrefixes {
		if long {
			fmt.Printf("%-12s  %s\n", "<folder>", p)
		} else {
			fmt.Println(p)
		}
	}
	for _, o := range resp.Objects {
		if long {
			fmt.Printf("%-12s  %s  %s\n",
				formatBytes(o.Size), o.LastModified.Format("2006-01-02 15:04"), o.Key)
		} else {
			fmt.Println(o.Key)
		}
	}
	if resp.Truncated {
		fmt.Fprintln(os.Stderr, "(truncated — pass --recursive or paginate)")
	}
	return nil
}

// splitBucketKey takes "bucket" or "bucket/some/key" and returns (bucket, "some/key").
// Trailing slash on the prefix is preserved.
func splitBucketKey(s string) (string, string) {
	i := strings.Index(s, "/")
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i+1:]
}
