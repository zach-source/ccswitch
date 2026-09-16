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
  ccswitch add-account                Add current account to managed accounts
  ccswitch remove-account <N|email>   Remove an account
  ccswitch current                    Show the current active account
  ccswitch list                       List all managed accounts
  ccswitch switch                     Pick an account interactively
  ccswitch switch-to <N|email>        Switch to a specific account
  ccswitch login [--only X] [--force] Re-authenticate expired accounts
  ccswitch refresh-all                Refresh expired OAuth tokens
  ccswitch prune [--apply]            Clear orphaned login-capture keychain records

Codex CLI:
  ccswitch codex add <label>          Capture the current Codex login as a profile
  ccswitch codex use <label>          Make a captured profile the live login
  ccswitch codex list                 List captured Codex profiles
  ccswitch codex current              Show the live Codex login
  ccswitch codex remove <label>       Forget a profile (the live login is untouched)

Usage Monitoring:
  ccswitch usage                      Show 5h block and weekly usage (active account)
  ccswitch usage-all [--json]         Show real usage for ALL accounts via API
  ccswitch set-limit <tokens>         Set weekly token limit (e.g. 6700M)

API Configuration:
  ccswitch use-zai                    Switch to the z.ai API endpoint
  ccswitch use-anthropic              Revert to the default Anthropic API
  ccswitch api-status                 Show current API configuration

Sync & Config:
  ccswitch sync [--quiet]             Bi-directional 1Password sync
  ccswitch push [--force]             Push local credentials to the backend
  ccswitch pull                       Pull credentials from the backend
  ccswitch daemon                     Run the sync loop continuously
  ccswitch config                     Print effective configuration
  ccswitch setup-op-connect           Interactive 1Password Connect setup
  ccswitch setup-op-vault             Create a dedicated vault + service-account setup
```

Every subcommand also accepts the legacy double-dashed form
(`ccswitch --switch-to 2`) so existing scripts and integrations keep working.

## How It Works

### Account Switching
ccswitch stores OAuth credentials per account in the macOS Keychain (or a
credentials file on Linux/WSL). When switching, it swaps the active
credentials and records the change. The "currently active" account is read
from `~/.claude.json` — the live source of truth — so ccswitch stays correct
even when you log in through `claude` directly.

### Headless 1Password Access
`ccswitch setup-op-vault` creates a dedicated vault and prints the commands
that grant a 1Password **service account** access to it. A service-account
token authenticates with no biometric prompt and no desktop app, which is what
`ccswitch daemon` under launchd/systemd, or a CI runner, needs.

A dedicated vault is not optional here: 1Password refuses to grant a service
account access to a Private or Personal vault, so the default `Private` vault
can never be reached headlessly. The grant ccswitch prints is
`read_items,write_items` on that one vault — enough to refresh and push, and
nothing else.

```bash
ccswitch setup-op-vault                  # create the vault, print the setup
ccswitch setup-op-vault --print-only     # just the instructions
```

Minting the service account is left to you: 1Password returns the token
exactly once and creating one needs owner or admin rights, so the token never
passes through ccswitch. Set `OP_SERVICE_ACCOUNT_TOKEN` in the environment of
whatever runs ccswitch and the `op` backend uses it automatically — the
configured account shorthand and the interactive `op signin` are both skipped
when that variable is present.

### Codex CLI Accounts
Codex keeps one login at a time in `$CODEX_HOME/auth.json` (default
`~/.codex`). `ccswitch codex` captures that file into a named profile stored
in the same backend as your Claude accounts — Keychain, file, 1Password, or
Vault — and restores it on demand. Switching saves the login it replaces
first, so a token Codex rotated on its own is never rolled back.

ccswitch never mints or exchanges a Codex token; Codex refreshes its own. The
only writes are a whole-file save and restore, so an upstream schema change
cannot corrupt a profile. Codex builds configured with
`cli_auth_credentials_store = "keyring"` keep the login in the OS keyring
rather than `auth.json`; ccswitch cannot address that and refuses rather than
silently capturing nothing.

### Usage Monitoring
`usage-all` queries the Anthropic OAuth usage API (`/api/oauth/usage`) for
each account using stored tokens — no switching required. It returns real
server-side 5h and 7d utilization percentages.

`usage` shows detailed stats for the active account, including the 5h block
breakdown, via `ccusage`.

## Requirements

ccswitch is a single self-contained Go binary. It shells out to a few tools
only when the relevant feature is used:

- `claude` — for `login` / `refresh-all` (OAuth token refresh)
- `ccusage` — for `usage` (local block/weekly stats)
- `op` — for `use-zai` (z.ai token retrieval from 1Password)
- `fzf` — optional, for the interactive `switch` picker
- macOS (Keychain) or Linux/WSL (credentials file)

## Development

```bash
make build        # compile ./bin/ccswitch
make check        # go vet + Go unit tests
make smoke        # bats CLI smoke tests against the built binary
make conformance  # full regression gate: vet + Go tests + bats — run before every change
```

`make conformance` is the gate to run before committing a change. The Go
tests and the bats suite both run hermetically — an isolated `$HOME` with the
file backend forced — so they never touch the real keychain, 1Password, or
network.

## License

MIT
