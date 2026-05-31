package commands

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/personals3/cli/internal/client"
	"github.com/personals3/cli/internal/config"
)

// Cp transfers between local file and remote object (either direction).
//
//   ps3 cp ./local.jpg my-bucket/photos/cat.jpg     # upload
//   ps3 cp my-bucket/photos/cat.jpg ./local.jpg     # download
//   ps3 cp my-bucket/photos/cat.jpg -                # download to stdout
//   ps3 cp - my-bucket/photos/cat.jpg                # upload from stdin (need --content-length)
//
// Remote paths are identified by containing a non-leading "/" with the
// bucket name as the first segment AND being non-existent as a local file.
// To force remote interpretation, prefix with "@" — e.g. "@my-bucket/cat.jpg".
func Cp() *cli.Command {
	return &cli.Command{
		Name:  "cp",
		Usage: "Copy between local files and remote objects (either direction)",
		ArgsUsage: "<src> <dst>\n\n" +
			"   Examples:\n" +
			"     ps3 cp ./photo.jpg my-bucket/photos/photo.jpg\n" +
			"     ps3 cp ./photo.jpg my-bucket/photos/      (auto-derives key: photos/photo.jpg)\n" +
			"     ps3 cp my-bucket/file.mp4 ~/out.mp4\n" +
			"     ps3 cp -r ./folder my-bucket/prefix/      (recursive — wraps `ps3 sync`)\n" +
			"\n" +
			"   For ongoing one-way mirror of a directory, prefer `ps3 sync`.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "content-type",
				Usage: "Override Content-Type for upload (default: inferred from extension)"},
			&cli.BoolFlag{Name: "recursive", Aliases: []string{"r"},
				Usage: "Copy a directory recursively (calls the same code as `ps3 sync` " +
					"under the hood; no --delete)"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			args := cmd.Args()
			if args.Len() != 2 {
				return fmt.Errorf("cp requires exactly two args: <src> <dst>")
			}
			src := args.Get(0)
			dst := args.Get(1)

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			cfg.MustServer()
			c := client.New(cfg)

			srcRemote := isRemote(src)
			dstRemote := isRemote(dst)
			recursive := cmd.Bool("recursive")

			// If src is a local directory, force recursive (or error if not asked).
			if !srcRemote {
				if info, err := os.Stat(stripAt(src)); err == nil && info.IsDir() {
					if !recursive {
						return fmt.Errorf("%q is a directory — pass -r to copy it, "+
							"or use `ps3 sync` for ongoing one-way mirroring", src)
					}
					if !dstRemote {
						return fmt.Errorf("destination must be a remote bucket/prefix when copying a directory")
					}
					return cpRecursiveUpload(ctx, c, stripAt(src), stripAt(dst))
				}
			}

			switch {
			case !srcRemote && dstRemote:
				// Trailing slash on the destination → derive the key from the source basename.
				if strings.HasSuffix(dst, "/") {
					dst = dst + filepath.Base(stripAt(src))
				}
				return uploadFile(ctx, c, strings.TrimPrefix(src, "@"), stripAt(dst),
					cmd.String("content-type"))
			case srcRemote && !dstRemote:
				// Mirror: trailing slash on local dst → save under that dir with source basename.
				if strings.HasSuffix(dst, "/") {
					dst = dst + filepath.Base(stripAt(src))
				}
				return downloadFile(ctx, c, stripAt(src), strings.TrimPrefix(dst, "@"))
			case srcRemote && dstRemote:
				return fmt.Errorf("remote→remote copy not supported yet — download then upload")
			default:
				return fmt.Errorf("both args look local — at least one must be a remote bucket/key")
			}
		},
	}
}

// cpRecursiveUpload uploads a local directory tree into a remote prefix.
// Reuses the sync code path so behaviour stays consistent. No --delete:
// the cp surface should never remove anything on the destination.
//
// Auto-picks up .ps3ignore in the source root (same as sync). This means
// `cp -r ./dir bucket/prefix/` and `sync ./dir bucket/prefix` skip the
// same files for a given directory.
func cpRecursiveUpload(ctx context.Context, c *client.Client, local, remote string) error {
	bucket, prefix := splitBucketKey(remote)
	if bucket == "" {
		return fmt.Errorf("remote must be bucket/prefix (got %q)", remote)
	}
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	// Auto-detect .ps3ignore at the local root — same convention as sync.
	var excludes, includes []string
	if _, err := os.Stat(filepath.Join(local, ".ps3ignore")); err == nil {
		ex, in, err := readIgnoreFile(filepath.Join(local, ".ps3ignore"))
		if err == nil {
			excludes = ex
			includes = in
			fmt.Fprintf(stderrCounter(),
				"  loaded %d pattern(s) from %s/.ps3ignore\n",
				len(ex)+len(in), local)
		}
	}

	files, err := walkLocal(local, true, excludes, includes)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderrCounter(), "uploading %d file(s) → %s/%s\n",
		len(files), bucket, prefix)
	for i, f := range files {
		key := prefix + f.RelPath
		if err := uploadOne(ctx, c, bucket, uploadTask{
			LocalPath: f.AbsPath,
			RemoteKey: key,
			Size:      f.Size,
		}); err != nil {
			fmt.Fprintf(stderrCounter(), "  [%d/%d] FAIL %s: %v\n",
				i+1, len(files), key, err)
			continue
		}
		fmt.Fprintf(stderrCounter(), "  [%d/%d] %s (%s)\n",
			i+1, len(files), key, formatBytes(f.Size))
	}
	return nil
}

// stripAt removes a leading "@" used to force-remote interpretation.
func stripAt(s string) string { return strings.TrimPrefix(s, "@") }

// isRemote returns true if `s` looks like a remote path. Order matters:
//
//  1. "-"                  → stdin/stdout (local)
//  2. starts with "@"      → forced remote (e.g. "@my-bucket/cat.jpg")
//  3. absolute / relative-path prefixes ("/" "~" "./" "../") → local
//  4. existing local file  → local (covers upload sources)
//  5. contains "/"         → remote (bucket/key form)
//  6. otherwise            → local (a bare filename)
//
// Without rule 3, a download destination like "/tmp/out.txt" gets
// misclassified as remote because the file doesn't exist YET (we're about
// to create it).
func isRemote(s string) bool {
	if s == "-" {
		return false
	}
	if strings.HasPrefix(s, "@") {
		return true
	}
	if strings.HasPrefix(s, "/") ||
		strings.HasPrefix(s, "~") ||
		strings.HasPrefix(s, "./") ||
		strings.HasPrefix(s, "../") ||
		s == "." || s == ".." {
		return false
	}
	if _, err := os.Stat(s); err == nil {
		return false
	}
	return strings.Contains(s, "/")
}

func uploadFile(ctx context.Context, c *client.Client, localPath, remote, ct string) error {
	bucket, key := splitBucketKey(remote)
	if bucket == "" || key == "" {
		return fmt.Errorf("upload destination must be bucket/key, got %q", remote)
	}

	// Stdin path stays as single PUT — no size, can't split into parts.
	if localPath == "-" {
		u := "/api/" + url.PathEscape(bucket) + "/" + client.EncodeKey(key)
		if err := c.Upload(ctx, u, os.Stdin, -1, ct); err != nil {
			return err
		}
		fmt.Printf("uploaded - → %s\n", remote)
		return nil
	}

	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	if ct == "" {
		ct = guessContentType(localPath)
	}

	// Multipart for big files: 4-way parallel parts with auto-sized chunks.
	// Falls back to single PUT under the 8 MiB threshold.
	cfg := client.MultipartConfig{
		PartSize:    client.PartSizeForFile(size),
		Concurrency: 4,
		Threshold:   8 * 1024 * 1024,
		Progress: func(done, total int64) {
			pct := 0
			if total > 0 {
				pct = int(done * 100 / total)
			}
			fmt.Fprintf(os.Stderr, "\ruploading %s: %3d%% (%s / %s)",
				filepath.Base(localPath), pct, formatBytes(done), formatBytes(total))
		},
	}
	_, err = c.UploadAuto(ctx, bucket, key, ct, f, size, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr) // newline after progress
		return err
	}
	fmt.Fprintln(os.Stderr) // newline after progress
	fmt.Printf("uploaded %s → %s (%s)\n", localPath, remote, formatBytes(size))
	return nil
}

func downloadFile(ctx context.Context, c *client.Client, remote, localPath string) error {
	bucket, key := splitBucketKey(remote)
	if bucket == "" || key == "" {
		return fmt.Errorf("download source must be bucket/key, got %q", remote)
	}

	var w io.Writer = os.Stdout
	var closer io.Closer
	if localPath != "-" {
		f, err := os.Create(localPath)
		if err != nil {
			return err
		}
		w = f
		closer = f
	}
	u := "/api/" + url.PathEscape(bucket) + "/" + client.EncodeKey(key)
	n, err := c.Download(ctx, u, w)
	if closer != nil {
		_ = closer.Close()
	}
	if err != nil {
		return err
	}
	if localPath != "-" {
		fmt.Fprintf(os.Stderr, "downloaded %s → %s (%s)\n", remote, localPath, formatBytes(n))
	}
	return nil
}

// guessContentType picks a basic MIME type from file extension. The server
// further normalizes generic types via its own mime map.
func guessContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg": return "image/jpeg"
	case ".png":          return "image/png"
	case ".gif":          return "image/gif"
	case ".webp":         return "image/webp"
	case ".mp4":          return "video/mp4"
	case ".mkv":          return "video/x-matroska"
	case ".mov":          return "video/quicktime"
	case ".mp3":          return "audio/mpeg"
	case ".flac":         return "audio/flac"
	case ".wav":          return "audio/wav"
	case ".pdf":          return "application/pdf"
	case ".json":         return "application/json"
	case ".txt", ".log":  return "text/plain"
	}
	return "application/octet-stream"
}
