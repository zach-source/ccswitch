# ccswitch

Multi-account switcher and usage monitor for Claude Code.

Manage multiple Claude Code accounts, switch between them, monitor real-time usage limits (5h block / 7d weekly), and configure API endpoints — all from the command line.

## Features

- Switch between multiple Claude Code OAuth accounts
- Real server-side usage monitoring via Anthropic API (5h and 7d limits)
- Progress bars with color-coded usage percentages
- z.ai enterprise API endpoint switching
- macOS Keychain credential management
- Linux/WSL credential file management

## Install

### Nix (flake)

```bash
# Run directly
nix run github:zach-source/ccswitch

# Add to flake inputs
{
  inputs.ccswitch.url = "github:zach-source/ccswitch";
  # ...
}

# Use the overlay
overlays = [ inputs.ccswitch.overlays.default ];
# Then: pkgs.ccswitch
```

### Homebrew

```bash
brew install zach-source/tap/ccswitch
```

### Manual

```bash
git clone https://github.com/zach-source/ccswitch.git
cd ccswitch
make install
```

## Usage

```
Account Management:
  ccswitch --add-account               Add current account to managed accounts
  ccswitch --remove-account <N|email>  Remove account
  ccswitch --current                   Show current active account
  ccswitch --list                      List all managed accounts
  ccswitch --switch                    Rotate to next account
  ccswitch --switch-to <N|email>       Switch to specific account

Usage Monitoring:
  ccswitch --usage                     Show 5h block and weekly usage (active account)
  ccswitch --usage-all                 Show real usage for ALL accounts via API
  ccswitch --set-limit <tokens>        Set weekly token limit (e.g. 6700M)

API Configuration:
  ccswitch --use-zai                   Switch to z.ai API endpoint
  ccswitch --use-anthropic             Revert to default Anthropic API
  ccswitch --api-status                Show current API configuration
```

## How It Works

### Account Switching
ccswitch stores OAuth credentials per account in the macOS Keychain (or `~/.claude/.credentials.json` on Linux). When switching, it swaps the active credentials and updates `~/.claude.json`.

### Usage Monitoring
`--usage-all` queries the Anthropic OAuth usage API (`/api/oauth/usage`) for each account using stored tokens — no switching required. Returns real server-side 5h and 7d utilization percentages.

`--usage` shows detailed stats for the active account including 5h block breakdown via `ccusage`.

## Requirements

- bash 4.0+
- jq
- curl
- python3
- macOS (Keychain) or Linux/WSL

## License

MIT
