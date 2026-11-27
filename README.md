<div align="center">
  <img src="docs/tori.svg" alt="Tori" width="150">
  <p><strong>Switch between Claude Code, Codex, and Antigravity accounts from your terminal — with live quota bars.</strong></p>
  <p>Linux · macOS · Windows</p>

  <p>
    <a href="https://github.com/Anurag-Raut/tori/releases"><img src="https://img.shields.io/github/v/release/Anurag-Raut/tori?style=flat-square" alt="Release"></a>
    <a href="https://github.com/Anurag-Raut/tori/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/Anurag-Raut/tori/ci.yml?branch=main&style=flat-square&label=CI" alt="CI"></a>
    <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/Anurag-Raut/tori?style=flat-square" alt="Go Version"></a>
    <a href="LICENSE"><img src="https://img.shields.io/github/license/Anurag-Raut/tori?style=flat-square" alt="License"></a>
  </p>

  <img src="docs/screenshot.png" alt="Tori interactive terminal picker" width="820">
</div>

## Highlights

- **Three tools, one picker** — manage multiple **Claude Code**, **Codex CLI**, and **Antigravity CLI** (`agy`) accounts side by side.
- **Live quota and pace bars** — see remaining 5-hour and 7-day quota, reset timers, and whether you're ahead or behind pace for every saved Claude Code and Codex account. Antigravity meters are off until you turn them on, and cover only the signed-in account.
- **No proxy, no daemon** — Tori swaps your own Claude Code and Codex logins (files and OS keyring entries) only when you ask it to, and switches Antigravity through Google's own sign-in. Your tools keep talking directly to their providers.
- **Interactive TUI + scriptable CLI** — navigate with keyboard or mouse in the Bubble Tea picker, or automate workflows with subcommands and `--json`.
- **Linux, macOS, and Windows** — a single static Go binary for x64 and arm64, with zero runtime dependencies.

## Install

### npm (Linux, macOS & Windows)

```bash
npm install -g tori-switch
```

Or run without installing globally:

```bash
npx tori-switch
```

### Quick install (Linux & macOS)

```bash
curl -fsSL https://raw.githubusercontent.com/Anurag-Raut/tori/main/install.sh | sh
```

Installs `tori` to `~/.local/bin` (override with `PREFIX=/usr/local/bin` or pin a version with `VERSION=v0.3.0`). Make sure `~/.local/bin` is on your `PATH`.

### Windows binary

Download the `.zip` for your architecture (`windows_amd64` or `windows_arm64`) from [GitHub Releases](https://github.com/Anurag-Raut/tori/releases), extract `tori.exe`, and put it in a folder on your `PATH`.

### Go (any OS)

```bash
go install github.com/Anurag-Raut/tori@latest
```

### Prebuilt binaries & source

Archives for every platform are on [GitHub Releases](https://github.com/Anurag-Raut/tori/releases). To build from source (Go 1.24+):

```bash
git clone https://github.com/Anurag-Raut/tori.git
cd tori
go build -o tori .
```

## Quick start

For Claude Code and Codex, save each account once; Tori handles switching from there. You can run `tori` and press `a` for the guided add flow, or save accounts from the CLI:

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

### Antigravity

Tori works with the **Antigravity CLI (`agy`)** only. Sign-in and sign-out happen in Google's own program; Tori saves account names and never copies or writes your logins.

```bash
tori g install            # run Google's official installer (asks first)
tori g login work         # sign in with Google, save the account as "work"
tori g login personal     # sign in with your next account
tori g switch work        # switching is a new Google sign-in
tori g logout             # opens agy to run /logout; type /exit to come back
```

- **Every switch needs a sign-in.** `agy` prints a link; open it, choose the account, and paste the code back. Press `Ctrl+D` to cancel (`Ctrl+Z`, then `Enter`, on Windows).
- **No automatic rotation.** Name the account you want; `tori switch` and `b` in the picker skip Antigravity.
- **The `*` marks the account last signed in through Tori.** If you sign in or out directly in `agy`, Tori doesn't notice. Run `tori g add <name>` to correct it.
- **Quota is opt-in** (`tori usage on g`). Tori then reads the current `agy` login to show quota for the signed-in account. It never renews that login; if it has expired, open `agy`.

#### Upgrading from 0.3.0 or earlier

Antigravity Desktop, IDE, and T3 Code support has been removed. Your account names are kept, but the tokens older versions saved are no longer used. Delete them with:

```bash
tori g cleanup            # lists the files and asks first
```

### Platform support

| Tool | Linux | macOS | Windows | Notes |
|---|---|---|---|---|
| **Claude Code** | Supported | Supported | Supported | `.credentials.json` (Linux/Windows), macOS Keychain (`Claude Code-credentials`), `CLAUDE_CONFIG_DIR`, and `CLAUDE_SECURESTORAGE_CONFIG_DIR` |
| **Codex CLI** | Supported | Supported | Supported | ChatGPT OAuth & API keys via `auth.json` and OS keyring (`Codex Auth`). On Windows, Credential Manager's 2,560-byte limit fits API keys only; OAuth logins use `file` (default) or `auto` |
| **Antigravity CLI** | Supported | Supported | Supported | Sign-in through `agy` itself. Opt-in quota reads `agy`'s OS keyring entry or token file |

> **macOS Keychain note:** On macOS, the first time `claude` or `codex` reads a Keychain item written by Tori, or Tori reads `agy`'s item for quota, macOS may show a one-time "Allow access" prompt.

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
| `b` | Rotate Claude Code and Codex to their next account |
| `r` | Refresh usage now |
| `?` | Show keybindings and bar legend |
| `Esc` | Close active dialog |
| `q` / `Ctrl+C` | Quit |

## CLI reference

```bash
tori [--ascii]                # interactive picker
tori list                     # all accounts (--no-usage, --refresh, --json)
tori status                   # active account per tool
tori switch                   # rotate Claude Code and Codex to their next account
tori usage                    # show meter status per tool
tori usage on|off <c|x|g|all> # toggle quota checks (--yes skips confirmation)
tori --version                # print version
```

Tool prefixes: `c` / `claude` (Claude Code), `x` / `codex` (Codex CLI), `g` / `a` / `agy` / `antigravity` / `gemini` (Antigravity). Running `tori <tool>` by itself runs `list`.

```bash
tori c switch 2          # switch by slot number, email, or alias
tori c switch            # rotate to the next Claude account
tori x alias 1 Work      # set an alias (--unset to clear; omit args to list)
tori g remove 2          # forget a saved account (--yes skips prompt)
tori c list --json       # machine-readable JSON output
```

Every tool supports `list` (`ls`), `status`, `add [alias]`, `switch` (`sw`), `remove` (`rm`), `alias`, and `help`. Antigravity requires a name for `add` and `switch`, and also has `install`, `login`, `logout`, and `cleanup`.

## Configuration & environment

Preferences live in `~/.tori/config.json` and saved credentials under `~/.tori/{claude,codex}`. `~/.tori/antigravity` holds account names and cached quota only. On Windows, `~` is your user profile (`%USERPROFILE%`).

| Variable | Description |
|---|---|
| `TORI_HOME` | Override the storage directory (default: `~/.tori`) |
| `TORI_ASCII=1` | Use plain ASCII glyphs (same as `tori --ascii`; auto-enabled on `TERM=linux`) |
| `TORI_NO_ANIM=1` | Disable spinner animation |
| `NO_COLOR=1` | Monochrome output |
| `CLAUDE_CONFIG_DIR`, `CLAUDE_SECURESTORAGE_CONFIG_DIR`, `TORI_CLAUDE_CREDS`, `TORI_CLAUDE_CONFIG` | Override Claude Code credential/config paths |
| `CODEX_HOME`, `TORI_CODEX_AUTH` | Override Codex CLI auth path |
| `TORI_AGY_BIN` | Override the path to `agy` |
| `TORI_AGY_CLI_TOKEN` | Override the `agy` token file read by opt-in quota meters |

## Privacy & terms

- **Local only** — saved logins stay on your machine under `~/.tori/`, written atomically. Files are created with `0600` permissions on Linux and macOS; on Windows they inherit your folder's permissions. Never share that folder.
- **Direct connections** — Tori is not a proxy and runs no background service. When quota meters are enabled, requests go directly to Anthropic, OpenAI, and Google to read usage (and refresh expired Claude Code and Codex tokens when needed). Turning meters off (`tori usage off all` or `--no-usage`) stops Tori's own network traffic; Claude Code and Codex switching works offline. Antigravity sign-in and installation need a connection.
- **Antigravity note** — Google's [terms](https://antigravity.google/terms) and [FAQ](https://antigravity.google/docs/faq) restrict third-party tools, including wrapping `agy` and automating account switching, and accounts may be suspended. Using Google's own sign-in lowers that risk but is not approval from Google. Quota meters read your `agy` login and call Google's quota endpoints as an Antigravity client, so they are off by default; turn them on with `tori usage on g` (or `u` in the picker) only if you accept that risk.

## Development

```bash
go test -race ./...
go vet ./...
```

Issues and pull requests are welcome.

## License

MIT — see [LICENSE](LICENSE).
