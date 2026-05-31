package commands

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/personals3/cli/internal/client"
	"github.com/personals3/cli/internal/config"
)

// Sync mirrors a local directory to a remote bucket prefix.
//
//   ps3 sync ~/Pictures my-bucket/photos
//   ps3 sync --delete --dry-run ~/Pictures my-bucket/photos
//   ps3 sync --exclude '*.tmp' --exclude '.DS_Store' ~/src my-bucket/backup
//
// What it does:
//   1. Walks the local directory, collects file metadata (path, size, mtime)
//   2. Lists every remote object under the destination prefix
//   3. For each local file: if missing remotely OR sizes differ → upload
//   4. With --delete: any remote-only object that matches the prefix → deleted (trashed)
//
// What it does NOT do:
//   - Two-way merge (local-only direction; use rclone for bidirectional)
//   - Hash comparison (uses size + presence; explicit --checksum re-uploads everything
//     if you want belt-and-suspenders)
//   - Symlinks (followed by default; pass --no-follow-symlinks to skip)
//   - Server-side multipart for large files (single PUT — works to ~10 GB on
//     reasonable links; use the dashboard's multipart for bigger)
func Sync() *cli.Command {
	return &cli.Command{
		Name:      "sync",
		Usage:     "Mirror a local directory to a remote prefix (one-way)",
		ArgsUsage: "<local-dir> <bucket/prefix>",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "delete", Usage: "Remove remote files not present locally (trashed, not purged)"},
			&cli.BoolFlag{Name: "dry-run", Usage: "Print actions without making changes"},
			&cli.BoolFlag{Name: "checksum", Usage: "Compare MD5 instead of size+presence (slow but exact)"},
			&cli.BoolFlag{Name: "no-follow-symlinks", Usage: "Skip symlinks instead of following"},
			&cli.StringSliceFlag{Name: "exclude", Usage: "Glob to skip (repeatable)"},
			&cli.StringSliceFlag{Name: "include", Usage: "Glob — if any are set, ONLY matching files sync (repeatable)"},
			&cli.StringSliceFlag{Name: "ignore-file",
				Usage: "Read patterns from a file (.gitignore-style; repeatable). " +
					".ps3ignore in the source root is auto-detected if present."},
			&cli.BoolFlag{Name: "use-gitignore",
				Usage: "Also read patterns from .gitignore in the source root"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() != 2 {
				return fmt.Errorf("sync requires <local-dir> <bucket/prefix>")
			}
			local := cmd.Args().Get(0)
			remote := cmd.Args().Get(1)
			bucket, prefix := splitBucketKey(remote)
			if bucket == "" {
				return fmt.Errorf("remote must be bucket/prefix (got %q)", remote)
			}
			// Normalize prefix: empty → root; otherwise must end with "/"
			if prefix != "" && !strings.HasSuffix(prefix, "/") {
				prefix += "/"
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			cfg.MustServer()
			c := client.New(cfg)

			info, err := os.Stat(local)
			if err != nil {
				return fmt.Errorf("local: %w", err)
			}
			if !info.IsDir() {
				return fmt.Errorf("%s is not a directory — use `ps3 cp` for single files", local)
			}

			dryRun := cmd.Bool("dry-run")
			doDelete := cmd.Bool("delete")
			checksum := cmd.Bool("checksum")
			followSymlinks := !cmd.Bool("no-follow-symlinks")
			excludes := cmd.StringSlice("exclude")
			includes := cmd.StringSlice("include")

			// Layer in ignore patterns from files.
			ignoreFiles := cmd.StringSlice("ignore-file")
			if _, err := os.Stat(filepath.Join(local, ".ps3ignore")); err == nil {
				ignoreFiles = append([]string{filepath.Join(local, ".ps3ignore")}, ignoreFiles...)
			}
			if cmd.Bool("use-gitignore") {
				if _, err := os.Stat(filepath.Join(local, ".gitignore")); err == nil {
					ignoreFiles = append([]string{filepath.Join(local, ".gitignore")}, ignoreFiles...)
				}
			}
			for _, p := range ignoreFiles {
				ex, in, err := readIgnoreFile(p)
				if err != nil {
					return fmt.Errorf("read %s: %w", p, err)
				}
				excludes = append(excludes, ex...)
				includes = append(includes, in...)
				fmt.Fprintf(stderrCounter(), "  loaded %d pattern(s) from %s\n",
					len(ex)+len(in), p)
			}

			// 1. Walk local
			fmt.Fprintln(stderrCounter(), "scanning local...")
			locals, err := walkLocal(local, followSymlinks, excludes, includes)
			if err != nil {
				return fmt.Errorf("walk local: %w", err)
			}
			fmt.Fprintf(stderrCounter(), "  %d local file(s)\n", len(locals))

			// 2. List remote
			fmt.Fprintln(stderrCounter(), "listing remote...")
			remotes, err := listRemote(ctx, c, bucket, prefix)
			if err != nil {
				return fmt.Errorf("list remote: %w", err)
			}
			fmt.Fprintf(stderrCounter(), "  %d remote object(s) under %s/%s\n",
				len(remotes), bucket, prefix)

			// 3. Compute diff
			uploads, deletes, skipped := diffSync(locals, remotes, prefix, checksum, local)

			fmt.Fprintf(stderrCounter(),
				"plan: %d upload, %d delete, %d unchanged\n",
				len(uploads), len(deletes), skipped)
			if dryRun {
				for _, u := range uploads {
					fmt.Printf("UPLOAD %s → %s/%s\n", u.LocalPath, bucket, u.RemoteKey)
				}
				if doDelete {
					for _, k := range deletes {
						fmt.Printf("DELETE %s/%s\n", bucket, k)
					}
				}
				return nil
			}

			// 4. Execute
			for i, u := range uploads {
				if err := uploadOne(ctx, c, bucket, u); err != nil {
					fmt.Fprintf(stderrCounter(), "  [%d/%d] FAIL %s: %v\n",
						i+1, len(uploads), u.RemoteKey, err)
					continue
				}
				fmt.Fprintf(stderrCounter(), "  [%d/%d] %s (%s)\n",
					i+1, len(uploads), u.RemoteKey, formatBytes(u.Size))
			}
			if doDelete {
				for i, k := range deletes {
					if err := c.Do(ctx, "DELETE",
						"/api/"+url.PathEscape(bucket)+"/"+client.EncodeKey(k), nil, nil,
					); err != nil {
						fmt.Fprintf(stderrCounter(), "  delete [%d/%d] FAIL %s: %v\n",
							i+1, len(deletes), k, err)
						continue
					}
					fmt.Fprintf(stderrCounter(), "  delete [%d/%d] %s\n", i+1, len(deletes), k)
				}
			}
			fmt.Println("sync complete")
			return nil
		},
	}
}

// ---------- helpers ----------

type localFile struct {
	AbsPath string
	RelPath string // path relative to root, forward-slash-separated
	Size    int64
	ModTime time.Time
}

type uploadTask struct {
	LocalPath string
	RemoteKey string
	Size      int64
}

func walkLocal(root string, followSymlinks bool, excludes, includes []string) ([]localFile, error) {
	out := []localFile{}
	root = strings.TrimRight(root, string(filepath.Separator))
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}

		// Directories: if excluded, prune the whole subtree (HUGE win on
		// node_modules / venv / .git — we don't walk them at all).
		if info.IsDir() {
			if !matchFilters(rel, excludes, includes) {
				return filepath.SkipDir
			}
			return nil
		}
		if !followSymlinks {
			if lst, _ := os.Lstat(path); lst != nil && lst.Mode()&os.ModeSymlink != 0 {
				return nil
			}
		}
		if !matchFilters(rel, excludes, includes) {
			return nil
		}
		out = append(out, localFile{
			AbsPath: path,
			RelPath: rel,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
		return nil
	})
	return out, err
}

// matchFilters returns true if the path passes the filters (i.e., is INCLUDED).
//
// Semantics match `.gitignore`:
//
//   - A pattern WITHOUT "/" matches if ANY path segment (or the basename)
//     equals it or its glob.  So "venv" matches "a/b/venv/c.py".
//
//   - A pattern WITH "/" is anchored to the sync root. It matches if the
//     path equals it, starts with "<pattern>/", or its glob matches the
//     full relative path.  So "src/build" matches "src/build/x.o" but NOT
//     "outer/src/build/x.o".
//
//   - Excludes win over includes only when includes are non-empty: then a
//     path passes only if at least one include matches.  Without includes,
//     anything not excluded passes.
func matchFilters(path string, excludes, includes []string) bool {
	for _, p := range excludes {
		if matchIgnorePattern(p, path) {
			return false
		}
	}
	if len(includes) == 0 {
		return true
	}
	for _, p := range includes {
		if matchIgnorePattern(p, path) {
			return true
		}
	}
	return false
}

// matchIgnorePattern is the .gitignore-style matcher used by matchFilters.
func matchIgnorePattern(pattern, path string) bool {
	if pattern == "" {
		return false
	}
	if strings.Contains(pattern, "/") {
		// Anchored to the root: prefix match OR exact match OR glob match.
		if path == pattern || strings.HasPrefix(path, pattern+"/") {
			return true
		}
		if m, _ := filepath.Match(pattern, path); m {
			return true
		}
		return false
	}
	// Floating pattern: match against every segment + the basename.
	for _, seg := range strings.Split(path, "/") {
		if seg == pattern {
			return true
		}
		if m, _ := filepath.Match(pattern, seg); m {
			return true
		}
	}
	return false
}

type remoteObj struct {
	Key  string
	Size int64
	ETag string
}

func listRemote(ctx context.Context, c *client.Client, bucket, prefix string) ([]remoteObj, error) {
	out := []remoteObj{}
	q := url.Values{}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	q.Set("max-keys", "1000")

	var resp struct {
		Objects []struct {
			Key  string `json:"key"`
			Size int64  `json:"size"`
			ETag string `json:"etag"`
		} `json:"objects"`
		Truncated bool `json:"truncated"`
	}
	if err := c.Do(ctx, "GET",
		"/api/"+url.PathEscape(bucket)+"?"+q.Encode(), nil, &resp,
	); err != nil {
		return nil, err
	}
	for _, o := range resp.Objects {
		out = append(out, remoteObj{Key: o.Key, Size: o.Size, ETag: o.ETag})
	}
	if resp.Truncated {
		fmt.Fprintln(stderrCounter(),
			"warning: remote listing truncated at 1000; some objects may not be compared")
	}
	return out, nil
}

func diffSync(locals []localFile, remotes []remoteObj, prefix string,
	checksum bool, localRoot string,
) (uploads []uploadTask, deletes []string, skipped int) {
	remoteIdx := make(map[string]remoteObj, len(remotes))
	for _, r := range remotes {
		remoteIdx[r.Key] = r
	}
	seen := make(map[string]struct{}, len(locals))
	for _, l := range locals {
		key := prefix + l.RelPath
		seen[key] = struct{}{}
		if r, ok := remoteIdx[key]; ok {
			same := r.Size == l.Size
			if checksum && same {
				if md5hex(l.AbsPath) != r.ETag {
					same = false
				}
			}
			if same {
				skipped++
				continue
			}
		}
		uploads = append(uploads, uploadTask{
			LocalPath: l.AbsPath,
			RemoteKey: key,
			Size:      l.Size,
		})
	}
	for k := range remoteIdx {
		if _, ok := seen[k]; !ok {
			deletes = append(deletes, k)
		}
	}
	return
}

func md5hex(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// readIgnoreFile parses a .gitignore-style file. Returns (excludes, includes).
// Lines starting with "!" become includes (re-include after a broader exclude).
// Lines starting with "#" and blank lines are skipped.
//
// Notes on what we DON'T match the full .gitignore spec on:
//   - Patterns ending in "/" are treated as directory globs but we don't
//     special-case "match anywhere in the tree" semantics — they behave
//     like normal globs.
//   - Anchored patterns (leading "/") have the leading slash stripped;
//     they then match by basename or relative path like other globs.
// This subset covers the 95% case (skip node_modules, .git, *.tmp, etc.)
// without pulling in a full gitignore-parser dependency.
func readIgnoreFile(path string) (excludes, includes []string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		neg := strings.HasPrefix(line, "!")
		if neg {
			line = strings.TrimPrefix(line, "!")
		}
		line = strings.TrimPrefix(line, "/")
		line = strings.TrimRight(line, "/")
		if line == "" {
			continue
		}
		if neg {
			includes = append(includes, line)
		} else {
			excludes = append(excludes, line)
		}
	}
	return excludes, includes, sc.Err()
}

func uploadOne(ctx context.Context, c *client.Client, bucket string, u uploadTask) error {
	f, err := os.Open(u.LocalPath)
	if err != nil {
		return err
	}
	defer f.Close()
	urlPath := "/api/" + url.PathEscape(bucket) + "/" + client.EncodeKey(u.RemoteKey)
	return c.Upload(ctx, urlPath, f, u.Size, guessContentType(u.LocalPath))
}
