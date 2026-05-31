// ps3 — the PersonalS3 command-line client.
//
// Install:
//   cd cli && go install ./cmd/ps3
//
// Quick start:
//   ps3 login --server http://localhost:8080 --email you@example.com
//   ps3 bucket list
//   ps3 ls my-bucket
//   ps3 cp ./photo.jpg my-bucket/photos/photo.jpg
//   ps3 cp my-bucket/photos/photo.jpg ./out.jpg
//   ps3 search vacation
//   ps3 share my-bucket/photos/photo.jpg --expires 1h
//   ps3 trash empty
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/personals3/cli/internal/commands"
)

var version = "dev"

func main() {
	app := &cli.Command{
		Name:    "ps3",
		Usage:   "Command-line client for PersonalS3 self-hosted storage",
		Version: version,
		Commands: []*cli.Command{
			commands.Login(),
			commands.Logout(),
			commands.Whoami(),
			commands.Ls(),
			commands.Cp(),
			commands.Sync(),
			commands.Rm(),
			commands.Search(),
			commands.Share(),
			commands.Trash(),
			commands.Transcode(),
			commands.Auth(),
			commands.Bucket(),
			commands.Completion(),
			commands.CompletePath(),
		},
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "ps3:", err)
		os.Exit(1)
	}
}
