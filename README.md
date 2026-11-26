<div align="center">
  <img src="docs/tori.svg" alt="Tori" width="150">
  <p><strong>Switch between Claude Code, Codex, and Antigravity accounts from your terminal — with live quota bars for every account.</strong></p>

  <p>
    <a href="https://github.com/Anurag-Raut/tori/releases"><img src="https://img.shields.io/github/v/release/Anurag-Raut/tori?style=flat-square" alt="Release"></a>
    <a href="https://github.com/Anurag-Raut/tori/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/Anurag-Raut/tori/ci.yml?branch=main&style=flat-square&label=CI" alt="CI"></a>
    <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/Anurag-Raut/tori?style=flat-square" alt="Go Version"></a>
    <a href="LICENSE"><img src="https://img.shields.io/github/license/Anurag-Raut/tori?style=flat-square" alt="License"></a>
  </p>

  <img src="docs/screenshot.png" alt="Tori interactive terminal picker" width="820">
</div>

## Highlights

- **Three tools, one picker** — manage multiple **Claude Code**, **Codex CLI**, and **Antigravity** (T3 Code) logins side by side.
- **Live quota and pace bars** — see remaining 5-hour and 7-day quota, reset timers, and whether you're ahead or behind pace across every saved account.
- **No proxy, no daemon** — Tori swaps your own local login files atomically (`0600`) when you ask it to. Your tools keep talking directly to their providers.
- **Interactive TUI + scriptable CLI** — navigate with keyboard or mouse in the Bubble Tea picker, or automate workflows with subcommands and `--json`.
- **Single static binary** — built in Go with zero runtime dependencies.

## Install

### npm

```bash
npm install -g tori-cli
```

Or run without installing globally:

```bash
npx tori-cli
```

### Quick install (Linux & macOS)

```bash
curl -fsSL https://raw.githubusercontent.com/Anurag-Raut/tori/main/install.sh | sh
```

Installs `tori` to `~/.local/bin` (override with `PREFIX=/usr/local/bin` or pin a version with `VERSION=v0.2.0`). Make sure `~/.local/bin` is on your `PATH`.

### Go

```bash
go install github.com/Anurag-Raut/tori@latest
```

### Prebuilt binaries & source

Grab a release archive from [GitHub Releases](https://github.com/Anurag-Raut/tori/releases), or build from source (Go 1.24+):

```bash
git clone https://github.com/Anurag-Raut/tori.git
cd tori
go build -o tori .
```

## Quick start

Save each account once; Tori handles switching from there. You can run `tori` and press `a` for the guided add flow, or save accounts from the CLI:

### Claude Code

```bash
claude             # /login with your first account
tori c add Work
claude             # /login with your next account
tori c add Personal
```

### Codex CLI

```bash
codex login        # first account (ChatGPT OAuth or API key)
tori x add Work
codex login        # next account
tori x add Personal
```

Codex reads its login at startup, so restart `codex` after switching.

### Antigravity (T3 Code)

```bash
tori g add Primary # while signed in to Antigravity in T3 Code
tori g add Backup  # after signing in with your next account
```

The alias is optional — Tori picks up the Google email automatically when quota loads. Restart any running Antigravity session after switching.

### Platform support

| Tool | Linux | macOS | Notes |
|---|---|---|---|
| **Claude Code** | Supported | Not yet | macOS stores Claude logins in Keychain rather than `~/.claude/.credentials.json` |
| **Codex CLI** | Supported | Supported | Supports both ChatGPT OAuth and API-key logins |
| **Antigravity** | Supported | Supported | Reads T3 Code's local `acp_token.json` |

## Reading the quota bars

- **Fill = quota remaining** — neutral slate-blue when healthy, amber (`!`) at 20% left or under, and red (`✕`) at 0%.
- **Pace line (`│`) = time left until reset** — fill past `│` means quota to spare; fill short of `│` means you're burning faster than the window resets.
- **Responsive layout** — displays both quota windows side by side on wide terminals (`100+` columns), and collapses unselected rows to their tightest window on narrow terminals (`42–99` columns).
- **Caching & backoff** — readings are cached locally for 3 minutes, refreshes are deduplicated and rate-limited, and transient errors or HTTP 429 responses back off automatically while keeping your last known numbers.
- **Per-tool control** — toggle quota meters off for any tool with `u` in the picker or `tori usage off <c|x|g|all>` for zero network requests.

## Interactive picker

Run `tori` with no arguments to open the interactive picker. Mouse navigation is supported throughout (hover rows, click to select, double-click to switch, click footer hints or dialog choices).

| Key | Action |
|---|---|
| `↑` `↓` / `j` `k` / scroll | Move selection |
| `Enter` / double-click | Switch to the selected account (or add on an empty tool) |
| `Tab` | Jump to the next tool |
| `a` | Add or update an account |
| `d` / `Delete` | Remove the selected account (with confirmation) |
| `u` | Toggle usage meters for the selected tool |
| `b` | Rotate every tool with 2+ accounts to its next slot |
| `r` | Refresh usage now |
| `?` | Show keybindings and bar legend |
| `Esc` | Close active dialog |
| `q` / `Ctrl+C` | Quit |

## CLI reference

```bash
tori [--ascii]                # interactive picker
tori list                     # all accounts (--no-usage, --refresh, --json)
tori status                   # active account per tool
tori switch                   # rotate every tool to its next account
tori usage                    # show meter status per tool
tori usage on|off <c|x|g|all> # toggle quota checks (--yes skips confirmation)
tori --version                # print version
```

Tool prefixes: `c` / `claude` (Claude Code), `x` / `codex` (Codex CLI), `g` / `a` / `agy` / `antigravity` / `gemini` (Antigravity). Running `tori <tool>` by itself runs `list`.

```bash
tori c switch 2          # switch by slot number, email, or alias
tori c switch            # rotate to the next Claude account
tori x alias 1 Work      # set an alias (--unset to clear; omit args to list)
tori g remove 2          # remove a saved slot (--yes skips prompt)
tori c list --json       # machine-readable JSON output
```

Every tool supports `list` (`ls`), `status`, `add [alias]`, `switch` (`sw`), `remove` (`rm`), `alias`, and `help`.

## Configuration & environment

Preferences live in `~/.tori/config.json` and saved credentials under `~/.tori/{claude,codex,antigravity}`.

| Variable | Description |
|---|---|
| `TORI_HOME` | Override the storage directory (default: `~/.tori`) |
| `TORI_ASCII=1` | Use plain ASCII glyphs (same as `tori --ascii`; auto-enabled on `TERM=linux`) |
| `TORI_NO_ANIM=1` | Disable spinner animation |
| `NO_COLOR=1` | Monochrome output |
| `CLAUDE_CONFIG_DIR`, `TORI_CLAUDE_CREDS`, `TORI_CLAUDE_CONFIG` | Override Claude Code credential/config paths |
| `CODEX_HOME`, `TORI_CODEX_AUTH` | Override Codex CLI auth path |
| `TORI_AGY_TOKEN` | Override Antigravity `acp_token.json` path |

## Privacy & terms

- **Local only** — saved logins stay on your machine under `~/.tori/`, written atomically with `0600` permissions. Never share that folder.
- **Direct connections** — Tori is not a proxy and runs no background service. When quota meters are enabled, requests go directly to Anthropic, OpenAI, and Google to read usage (and refresh expired tokens when needed). Turning meters off (`tori usage off all` or `--no-usage`) stops all network traffic; switching, adding, and removing accounts work offline.
- **Antigravity note** — Google's terms prohibit third-party tools using Antigravity sign-in and may risk account suspension. Turn Antigravity meters off with `tori usage off g` if you prefer not to query Antigravity quotas.

## Development

```bash
go test -race ./...
go vet ./...
```

Issues and pull requests are welcome.

## License

MIT — see [LICENSE](LICENSE).
