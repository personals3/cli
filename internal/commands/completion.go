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

// ----- ps3 completion <shell> --------------------------------------------

// Completion prints shell init code that hooks tab-completion to ps3.
//
//   ps3 completion bash > /etc/bash_completion.d/ps3
//   ps3 completion zsh  > "${fpath[1]}/_ps3"   # or eval at startup
//
// The init code wires a function that calls `ps3 __complete-path <current>`
// for the argument under the cursor and feeds the lines back to the shell.
func Completion() *cli.Command {
	return &cli.Command{
		Name:      "completion",
		Usage:     "Print shell init for tab-completion (bash / zsh / fish)",
		ArgsUsage: "<shell>",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			shell := cmd.Args().First()
			switch shell {
			case "bash":
				fmt.Print(bashCompletion)
			case "zsh":
				fmt.Print(zshCompletion)
			case "fish":
				fmt.Print(fishCompletion)
			default:
				return fmt.Errorf("supported shells: bash, zsh, fish (got %q)", shell)
			}
			return nil
		},
	}
}

// ----- ps3 __complete-path <current> -------------------------------------

// CompletePath is invoked by the shell init scripts. Hidden from --help.
//
//   no "/" in current → list buckets matching the prefix
//   "bucket/path/"    → list folders + files under that prefix
//
// Output: one candidate per line on stdout. Errors silently drop to empty.
func CompletePath() *cli.Command {
	return &cli.Command{
		Name:   "__complete-path",
		Hidden: true,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			cur := cmd.Args().First()
			cfg, err := config.Load()
			if err != nil || cfg.Server == "" || cfg.Token == "" {
				return nil // no config = no completions; silent
			}
			c := client.New(cfg)

			if !strings.Contains(cur, "/") {
				// Buckets — list and filter by prefix.
				var r struct {
					Buckets []struct {
						Name string `json:"name"`
					} `json:"buckets"`
				}
				if err := c.Do(ctx, "GET", "/api/", nil, &r); err != nil {
					return nil
				}
				for _, b := range r.Buckets {
					if strings.HasPrefix(b.Name, cur) {
						// Trailing "/" so the next tab dives in.
						fmt.Println(b.Name + "/")
					}
				}
				return nil
			}

			// Object path: split into bucket + prefix.
			bucket, prefix := splitBucketKey(cur)
			if bucket == "" {
				return nil
			}
			// Find the last "/" — everything before is the locked prefix.
			lastSlash := strings.LastIndex(prefix, "/")
			parentPrefix := prefix
			if lastSlash >= 0 {
				parentPrefix = prefix[:lastSlash+1]
			} else {
				parentPrefix = ""
			}

			q := url.Values{}
			q.Set("delimiter", "/")
			if parentPrefix != "" {
				q.Set("prefix", parentPrefix)
			}
			q.Set("max-keys", "200")

			var r struct {
				Objects        []struct{ Key string } `json:"objects"`
				CommonPrefixes []string               `json:"commonPrefixes"`
			}
			if err := c.Do(ctx, "GET",
				"/api/"+url.PathEscape(bucket)+"?"+q.Encode(), nil, &r,
			); err != nil {
				return nil
			}

			// Folders first (with trailing slash)
			for _, p := range r.CommonPrefixes {
				cand := bucket + "/" + p
				if strings.HasPrefix(cand, cur) {
					fmt.Println(cand)
				}
			}
			for _, o := range r.Objects {
				cand := bucket + "/" + o.Key
				if strings.HasPrefix(cand, cur) {
					fmt.Println(cand)
				}
			}
			return nil
		},
	}
}

// ----- Init scripts (literal blobs) --------------------------------------

// ----- Init scripts (literal blobs) --------------------------------------
//
// Each shell's blob handles three cases:
//   1. Subcommand name itself (after "ps3 ") → list of known subcommands
//   2. Argument LOOKS LOCAL (starts with /, ~, ./, ../, or is empty) →
//      defer to native file completion
//   3. Argument LOOKS REMOTE (bucketname or bucketname/...) → ask
//      `ps3 __complete-path` for matching buckets/keys
//
// Commands that ONLY take remote paths (ls, rm, share, cat, stat) skip the
// local fallback. Commands that take both (cp, sync) try local-first when
// the word obviously looks local, otherwise try remote.

const bashCompletion = `# ps3 bash completion. Source via:
#   eval "$(ps3 completion bash)"
# or save to /etc/bash_completion.d/ps3
_ps3_complete() {
	local cur="${COMP_WORDS[COMP_CWORD]}"
	local sub="${COMP_WORDS[1]}"

	# Looks-local heuristic — defer to the shell's file completion.
	_ps3_local_complete() {
		# Make _filedir available; falls back to compgen if not loaded.
		if declare -f _filedir >/dev/null 2>&1; then
			_filedir
		else
			COMPREPLY=( $(compgen -f -- "$cur") )
		fi
	}

	_ps3_remote_complete() {
		COMPREPLY=()
		while IFS= read -r line; do
			COMPREPLY+=("$line")
		done < <(ps3 __complete-path "$cur" 2>/dev/null)
	}

	case "$sub" in
		ls|rm|share|cat|stat)
			_ps3_remote_complete
			return
			;;
		cp|sync)
			# Local-style path → local files; else try remote.
			case "$cur" in
				""|/*|~*|./*|../*|.|..)
					_ps3_local_complete
					return
					;;
			esac
			_ps3_remote_complete
			if [ "${#COMPREPLY[@]}" -eq 0 ]; then
				_ps3_local_complete
			fi
			return
			;;
	esac

	if [ "$COMP_CWORD" -eq 1 ]; then
		COMPREPLY=( $(compgen -W "login logout whoami ls cp sync rm search share trash bucket completion" -- "$cur") )
	fi
}
complete -F _ps3_complete ps3
`

const zshCompletion = `# ps3 zsh completion. Source via:
#   eval "$(ps3 completion zsh)"
# or save to a directory in $fpath as _ps3
_ps3() {
	local cur="${words[CURRENT]}"
	local sub="${words[2]}"

	# Use zsh's own _files for the local case — it picks up the user's
	# preferences (case-insensitivity, hidden files, etc).
	_ps3_local() { _files }

	_ps3_remote() {
		local -a opts
		opts=("${(@f)$(ps3 __complete-path "$cur" 2>/dev/null)}")
		if (( ${#opts[@]} )); then
			compadd -- "${opts[@]}"
			return 0
		fi
		return 1
	}

	case "$sub" in
		ls|rm|share|cat|stat)
			_ps3_remote
			return
			;;
		cp|sync)
			# Looks local? → file completion. Else → try remote, fall back to files.
			case "$cur" in
				""|/*|~*|./*|../*|.|..)
					_ps3_local
					return
					;;
			esac
			_ps3_remote || _ps3_local
			return
			;;
	esac

	if [ "$CURRENT" -eq 2 ]; then
		compadd -- login logout whoami ls cp sync rm search share trash bucket completion
	fi
}
compdef _ps3 ps3
`

const fishCompletion = `# ps3 fish completion. Source via:
#   ps3 completion fish | source
# or save to ~/.config/fish/completions/ps3.fish
function __ps3_complete_path
	set -l cur (commandline -ct)
	# Defer to fish's file completion for local-looking paths
	switch "$cur"
		case '' '/*' '~*' './*' '../*' '.' '..'
			return 1
	end
	ps3 __complete-path "$cur" 2>/dev/null
end

# Subcommand names
complete -c ps3 -n "__fish_use_subcommand" -a \
	"login logout whoami ls cp sync rm search share trash bucket completion"

# Remote-only commands (no local file completion)
complete -c ps3 -n "__fish_seen_subcommand_from ls rm share cat stat" \
	-a "(ps3 __complete-path (commandline -ct) 2>/dev/null)" -f

# cp / sync — remote suggestions PLUS native file completion (fish merges)
complete -c ps3 -n "__fish_seen_subcommand_from cp sync" \
	-a "(__ps3_complete_path)" -F
`
