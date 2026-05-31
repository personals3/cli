package commands

import (
	"context"
	"fmt"
	"net/url"

	"github.com/urfave/cli/v3"

	"github.com/personals3/cli/internal/client"
	"github.com/personals3/cli/internal/config"
)

// Transcode enqueues an HLS/preview transcode for one object, or restarts a
// previously-completed/failed transcode.
//
//   ps3 transcode my-bucket/movie.mp4              # enqueue (no-op if already done/pending)
//   ps3 transcode --restart my-bucket/movie.mp4    # wipe old segments + re-enqueue
//
// Wire-level:
//   default:    POST   /api/{bucket}/{key}?transcode
//   --restart:  DELETE /api/{bucket}/{key}?transcode   (wipes existing streams)
//               POST   /api/{bucket}/{key}?transcode   (re-enqueues)
//
// (The API exposes the wipe endpoint as ?transcode singular — same dispatch key
// as the enqueue, differentiated by HTTP method.)
func Transcode() *cli.Command {
	return &cli.Command{
		Name:      "transcode",
		Usage:     "Enqueue (or restart) an HLS transcode for one object",
		ArgsUsage: "<bucket/key>",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "restart",
				Usage: "Delete existing transcoded streams first, then re-enqueue"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			target := cmd.Args().First()
			if target == "" {
				return fmt.Errorf("transcode requires bucket/key")
			}
			bucket, key := splitBucketKey(target)
			if bucket == "" || key == "" {
				return fmt.Errorf("invalid target %q (need bucket/key)", target)
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			cfg.MustServer()
			c := client.New(cfg)

			u := "/api/" + url.PathEscape(bucket) + "/" + client.EncodeKey(key) + "?transcode"

			if cmd.Bool("restart") {
				// Wipe first — server reclaims disk + clears transcoded_bytes.
				if err := c.Do(ctx, "DELETE", u, nil, nil); err != nil {
					return fmt.Errorf("wipe transcodes: %w", err)
				}
			}

			var resp struct {
				ObjectID string `json:"objectId"`
				FileType string `json:"fileType"`
				Status   string `json:"status"`
			}
			if err := c.Do(ctx, "POST", u, nil, &resp); err != nil {
				return err
			}
			fmt.Printf("queued %s/%s (type=%s, status=%s)\n",
				bucket, key, resp.FileType, resp.Status)
			return nil
		},
	}
}
