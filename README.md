# ps3 — PersonalS3 CLI

Command-line client for [PersonalS3](https://personals3.tech), a self-hosted
S3-compatible object storage system with built-in media transcoding.

## Install

### macOS / Linux

```bash
curl -fsSL https://personals3.tech/install | sh
```

This detects your OS + architecture, downloads the matching binary from the
latest GitHub release, and installs `ps3` to `/usr/local/bin/` (or
`~/.local/bin/` if you can't write to system paths).

### Windows

Download the `ps3_<version>_windows_amd64.zip` archive from the
[Releases page](https://github.com/personals3/cli/releases/latest), extract
it, and add `ps3.exe` to a directory on your `PATH`.

### From source

```bash
go install github.com/personals3/cli/cmd/ps3@latest
```

## Quick start

```bash
# Sign in (opens a session for 24h)
ps3 login --server https://personals3.tech --email you@example.com

# Create a bucket
ps3 bucket create my-music

# Upload a file
ps3 cp song.mp3 my-music/song.mp3

# List
ps3 ls my-music/

# Generate a public share link (instant download)
ps3 share my-music/song.mp3 --ttl 1h

# Sync a directory
ps3 sync ./local-folder my-music/folder/
```

## Commands

| Command | What it does |
|---|---|
| `ps3 login` / `logout` / `whoami` | Auth session management |
| `ps3 authkeys`                   | Create + list API keys (for scripts and S3 SDKs) |
| `ps3 bucket {list,create,delete,patch}` | Bucket lifecycle |
| `ps3 ls`                         | List objects in a bucket / prefix |
| `ps3 cp`                         | Upload / download / copy objects (handles multipart) |
| `ps3 rm`                         | Delete objects (soft-delete by default) |
| `ps3 sync`                       | One-way directory sync (`local → remote` or reverse) |
| `ps3 share`                      | Generate signed share links |
| `ps3 search`                     | Full-text search across your buckets |
| `ps3 trash`                      | Browse / restore / purge the trash |
| `ps3 transcode`                  | Trigger / inspect transcode jobs |
| `ps3 completion`                 | Shell completion scripts (bash / zsh / fish) |

Run `ps3 <command> --help` for the full flag list on any subcommand.

## Configuration

Credentials and the configured server URL are stored at:

- Linux / macOS: `~/.config/ps3/config.yaml`
- Windows: `%APPDATA%\ps3\config.yaml`

You can override the server per-invocation with `--server`, or set
`PS3_SERVER` in your environment.

## License

[MIT](./LICENSE)
