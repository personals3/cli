package commands

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/personals3/cli/internal/client"
	"github.com/personals3/cli/internal/config"
)

// Search hits the /search endpoint with the same filters the dashboard
// exposes. Output: bucket/key TAB size TAB lastModified per line, so it's
// pipe-able into xargs / cut / etc.
func Search() *cli.Command {
	return &cli.Command{
		Name:      "search",
		Usage:     "Cross-bucket search by key substring + filters",
		ArgsUsage: "[query]",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "bucket", Usage: "restrict to one bucket"},
			&cli.StringFlag{Name: "type", Usage: "content-type prefix (e.g. image, video/mp4)"},
			&cli.StringFlag{Name: "ext", Usage: "file extension (e.g. jpg)"},
			// In urfave/cli v3 IntFlag is already int64-typed.
			&cli.IntFlag{Name: "min-size", Usage: "minimum size in bytes"},
			&cli.IntFlag{Name: "max-size", Usage: "maximum size in bytes"},
			&cli.IntFlag{Name: "limit", Value: 100, Usage: "max results"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			cfg.MustServer()
			c := client.New(cfg)

			q := url.Values{}
			if s := cmd.Args().First(); s != "" {
				q.Set("q", s)
			}
			if s := cmd.String("bucket"); s != "" { q.Set("bucket", s) }
			if s := cmd.String("type");   s != "" { q.Set("type", s) }
			if s := cmd.String("ext");    s != "" { q.Set("ext", s) }
			if n := cmd.Int("min-size"); n > 0 { q.Set("minSize", strconv.FormatInt(n, 10)) }
			if n := cmd.Int("max-size"); n > 0 { q.Set("maxSize", strconv.FormatInt(n, 10)) }
			q.Set("limit", strconv.FormatInt(cmd.Int("limit"), 10))

			var resp struct {
				Total   int `json:"total"`
				Results []struct {
					Bucket       string    `json:"bucket"`
					Key          string    `json:"key"`
					Size         int64     `json:"size"`
					ContentType  string    `json:"contentType"`
					LastModified time.Time `json:"lastModified"`
				} `json:"results"`
			}
			if err := c.Do(ctx, "GET", "/api/search?"+q.Encode(), nil, &resp); err != nil {
				return err
			}
			for _, r := range resp.Results {
				fmt.Printf("%s/%s\t%s\t%s\t%s\n",
					r.Bucket, r.Key, formatBytes(r.Size),
					r.LastModified.Format("2006-01-02 15:04"), r.ContentType)
			}
			if resp.Total > len(resp.Results) {
				fmt.Fprintf(stderrCounter(), "... %d more matches (raise --limit)\n",
					resp.Total-len(resp.Results))
			}
			return nil
		},
	}
}
