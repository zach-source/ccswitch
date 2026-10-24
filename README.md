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
  ccswitch codex login <email>        Sign in an account in its Chrome profile and save it
  ccswitch codex add-key <label>      Save an OpenAI API key from stdin as a profile
  ccswitch codex priority [label...]  Show or set the autoswitch order
  ccswitch codex usage [--json]       Show rate-limit usage for each profile
  ccswitch codex autoswitch --once    Switch the live login when it is near its limit

Long-lived tokens:
  ccswitch token create <email|N>     Sign in and save a long-lived token (about 1 year)
  ccswitch token get <email|N>        Print the token, for CLAUDE_CODE_OAUTH_TOKEN
  ccswitch token list                 List saved tokens with their age (no token text)
  ccswitch token remove <email|N>     Delete a saved token
  ccswitch token create --codex <email>               Sign in and save a Codex token session
  ccswitch token get --codex <email> [--codex-home D]  Print a Codex auth.json (no refresh token), or write D/auth.json
  ccswitch token remove --codex <email>               Revoke and delete a Codex token session

Usage Monitoring:
  ccswitch usage                      Show 5h block and weekly usage (active account)
  ccswitch usage-all [--json]         Show real usage for ALL accounts (and Codex profiles) via API
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

`ccswitch codex use` saves the live login into the profile with the same
account ID and email, not into the profile it made active last. A Codex session
that started before a switch can write its own tokens back into `auth.json`, so
this stops one account's login from being saved under another account. If the
live login matches no profile, `codex use` stops and changes nothing. If the
store write fails, ccswitch keeps a local copy. Reads use the copy with the
later `last_refresh`.

`ccswitch codex login <email>` runs `codex login --device-auth` in a temporary
`CODEX_HOME`, so the live login does not change. It prints the one-time code
and opens the sign-in page in the Chrome profile that is signed in to
`<email>`. If no Chrome profile matches, it prints the address. It saves the
login only when the signed-in email is `<email>`, and adds the profile to the
end of the priority order.

`ccswitch codex add-key <label>` reads an OpenAI API key from stdin (or a no-echo prompt on a terminal) and saves it as a profile for CI and scripts. Codex writes the login in a temporary `CODEX_HOME`, and the live login does not change. Run `ccswitch codex use <label>` to make it live. API-key profiles come last in the priority order and autoswitch never picks them.

`ccswitch codex usage` reads `https://chatgpt.com/backend-api/wham/usage` for
each profile at the same time. A profile whose access token has expired shows
`stale` and sends no request, because ccswitch does not refresh Codex tokens.
The text output has the same bars as `usage-all`: one line for each window
(`5h`, `7d`) with a color-coded percentage and the time until reset. `◉` marks
the live profile. `--json` output is not changed.

`ccswitch codex autoswitch --once [--threshold 80] [--cooldown 5m]` moves the
live login to the first profile in priority order that is under the threshold.
A `promax` profile always comes before a `pro` profile. The priority order
applies between profiles with the same plan.
Codex reads its login at session start, so a switch applies to the next
session. Run it from a Codex `SessionStart` hook.

### Tokens for scripts and CI
Scripts and CI cannot use a browser login. `ccswitch token create <email|N>`
runs `claude setup-token` for that account. The sign-in opens in the Chrome
profile of the account, as `ccswitch login` does. Claude prints a token that
lasts about one year. ccswitch saves it in your backend as `Claude-Token-<email>`
and does not print it. It needs `script(1)`, which macOS and most Linux systems
have.

```
ccswitch token create work@example.com
export CLAUDE_CODE_OAUTH_TOKEN=$(ccswitch token get work@example.com)
claude -p "summarize this repo"
```

`token get` is the only command that prints a token. `token list` shows the
created date and age. `token remove` deletes the saved copy. It does not revoke
the token.

#### Codex sessions for scripts and CI
`CODEX_ACCESS_TOKEN` accepts only a Business or Enterprise access token, so it
does not help on other plans. Add `--codex` to give any plan an `auth.json`:

```
ccswitch token create --codex work@example.com
ccswitch token get --codex work@example.com --codex-home "$CODEX_HOME"
```

`token create --codex` runs a device sign-in in a temporary `CODEX_HOME`. It
saves the result as `Codex-Token-<email>` and does not touch your live Codex
login or your Codex profiles. This session is separate, and only ccswitch
holds its refresh token. `token get --codex` and `refresh-all` renew it when
the access token has less than 24 hours left. They renew only these dedicated
sessions, never your Codex profiles.

The file that `token get --codex` prints has `refresh_token` set to `""`. CI
cannot rotate the session, so it cannot break it. The access token lasts 10
days, so a copy is good for at least 24 hours. `token list` shows Codex rows
for the emails of your Codex profiles. `token remove --codex` revokes the
session and deletes the saved copies.

### Usage Monitoring
`usage-all` queries the Anthropic OAuth usage API (`/api/oauth/usage`) for
each account using stored tokens — no switching required. It returns real
server-side 5h and 7d utilization percentages. When Codex profiles exist,
`usage-all` also prints a "Codex Accounts" section after the Claude accounts.
A Codex error prints one warning line and does not change the exit code.
`usage-all --json` adds a `codex` field with the same rows as
`codex usage --json`.

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
