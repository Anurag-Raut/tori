<p align="center"><img src="docs/tori.svg" alt="Tori" width="150"></p>

Tori is a fast terminal account switcher for **Claude Code**, **Codex** and **Antigravity**, with built-in quota meters for every saved account (on by default, see [Usage meters](#usage-meters)).

![Tori interactive picker](docs/screenshot.png)

## Install

### Quick install (Linux & macOS)

```bash
curl -fsSL https://raw.githubusercontent.com/Anurag-Raut/tori/main/install.sh | sh
```

This installs the `tori` binary to `${PREFIX:-$HOME/.local/bin}`. Make sure `~/.local/bin` is on your `PATH`.

### Go install

```bash
go install github.com/Anurag-Raut/tori@latest
```

## Setup

Save each account once; Tori then switches between them.

### Claude Code

```bash
claude            # /login as your first account
tori c add Work
claude            # /login as the next account
tori c add Personal
```

> **macOS note:** On macOS, Claude Code stores its login in the Keychain rather than `~/.claude/.credentials.json`, which Tori does not support yet.

### Codex

```bash
codex login       # first account
tori x add Work
codex login       # next account (after logging out)
tori x add Personal
```

Codex reads its login at startup, so **restart codex after switching**.

### Antigravity

```bash
tori g add primary      # while signed in to Antigravity (in T3 Code)
tori g add backup       # after signing in with the next account
```

The alias is optional; the Google email is read the first time usage loads. Restart any running Antigravity session after switching.

## Picker keys & mouse

Run `tori` with no arguments to open the interactive Bubble Tea picker (supports keyboard and mouse). Use `tori --ascii`, `TORI_ASCII=1` (auto-enabled on `TERM=linux`) for pure ASCII glyphs, `NO_COLOR=1` for monochrome output, and `TORI_NO_ANIM=1` for a static loading indicator.

| Key / Mouse | Action |
|---|---|
| `↑` `↓` / `j` `k` / scroll wheel | Move selection |
| Hover / Click / Double-click | Hover rows, click to select, double-click to switch, click footer shortcuts or dialog choices |
| `Enter` | Switch to the selected account (or add on an empty tool) |
| `Tab` | Jump to the first account of the next tool |
| `a` | Guided add account flow (pick tool, sign in & re-check, update existing slot, or save with optional alias) |
| `d` / `Delete` | Guided remove (safe default: never removes on a single keypress; offers switch-and-remove or clear login when active) |
| `u` | Turn meters on or off for the selected tool |
| `b` | Rotate every tool with 2 or more saved accounts |
| `r` | Refresh usage now |
| `?` | Show full key list and bar legend |
| `Esc` | Close active dialog |
| `q` / `Ctrl+C` | Quit (`Ctrl+C` always works) |

## Usage meters

Meters are **on by default** and can be turned off per tool any time with `u` in the picker or `tori usage off c` (also `x`, `g`, `all`). Choices are saved in `~/.tori/config.json`. While a tool's meters are off, Tori makes zero network requests for it. Switching, adding and removing accounts never need the network.

In the picker, each account fits on one line on wide terminals (100+ columns) with both quota windows side by side, and collapses to the lowest remaining window on narrow terminals (42–99 columns) while expanding the selected account. Bars show percent **left** in neutral gray-blue, turning amber at 20% left (`!`) and red at 0% (`✕`). The white `│` pace line marks the share of the window still ahead until reset — fill past it means quota to spare, fill short of it means you are burning faster than time passes. `tori list` shows the percentage used. Readings are cached locally (180s TTL), manual refreshes enforce a 30s minimum cooldown with in-flight request deduplication, and HTTP 429 / transient errors trigger persisted exponential backoff (honoring `Retry-After`) while keeping last-known readings.

## CLI reference

```bash
tori [--ascii]         # interactive picker
tori list              # all accounts; meters if on (--no-usage hides, --refresh re-reads, --json)
tori usage             # show meters on/off per tool (on by default)
tori usage off all     # turn meters off (c|x|g|all)
tori usage on c        # turn meters back on; shows disclaimer, asks y/N (--yes skips)
tori status            # current account per tool
tori switch            # rotate every tool to its next account
tori --version         # print version
```

Per-tool prefixes: `c` / `claude` (Claude Code), `x` / `codex` (Codex), `g` / `a` / `agy` / `antigravity` (Antigravity). Bare `tori c` is `tori c list`.

```bash
tori c switch 2        # by slot number, email or alias
tori x alias 1 Work    # --unset to clear
tori g remove 2        # asks y/N; -y/--yes skips (remaining slots are renumbered)
tori c list --json     # machine-readable
```

Each tool takes `list`, `status`, `add [alias]`, `switch [num|email|alias]`, `remove <ref>`, `alias <ref> <name>` and `help`.

## Works with the official tools

Tori sits beside Claude Code, Codex and Antigravity, not between them and the provider. There is no proxy: your tools keep talking to their provider directly, and Tori only changes which of your own saved logins is in place when you ask it to. It never shares accounts, routes requests or runs in the background.

## Terms of use

When usage meters are on (the default), Tori reads quota directly from each provider using your saved logins (and may refresh expired tokens). Nothing is sent anywhere else, and you can turn meters off per tool any time (`u` in the picker or `tori usage off <c|x|g|all>`).

- **Antigravity**: Google's terms prohibit third-party tools using Antigravity sign-in and may lead to account suspension. Turn Antigravity meters off with `tori usage off g` if you prefer not to read Antigravity quotas.

## Privacy

- Saved logins live only on your machine, under `~/.tori/` (`claude`, `codex`, `antigravity`), written atomically with `0600` permissions. Never share that folder.
- With meters on, the only network calls go to each provider's own usage and token endpoints (Anthropic, OpenAI, Google), to read quotas and refresh an inactive account's expired token. With meters off (`tori usage off all` or `--no-usage`), Tori makes no network calls.
- Accounts saved by earlier versions (`~/.claudeswap`, `~/.codexswap`, `~/.aswap`) are moved into `~/.tori/` automatically on first run.
- Claude Code on macOS stores its login in the Keychain, which is currently unsupported.

## License

MIT, see [LICENSE](LICENSE).
