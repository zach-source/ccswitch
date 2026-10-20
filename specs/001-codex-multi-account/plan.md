# Implementation Plan: Codex Multi-Account Switching

**Branch**: `001-codex-multi-account` | **Date**: 2026-10-20 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/001-codex-multi-account/spec.md`

## Summary

ccswitch already saves and restores whole Codex `auth.json` files as named profiles. The commands are `ccswitch codex add/use/list/current/remove`. This feature adds four things:

1. A sign-in command. It opens the device sign-in page in the correct Chrome profile. It saves the result as a profile.
2. A usage command. It reads the usage windows of each profile.
3. An autoswitch command. It selects the live profile by priority.
4. A safe swap. ccswitch identifies the live login by account. It keeps a local copy if the store write fails.

ccswitch never renews a Codex token (see [research.md](research.md) R1).

## Technical Context

**Language/Version**: Go 1.26 (from `go.mod`)

**Primary Dependencies**: `github.com/spf13/cobra` (present). No new dependency.

**Storage**: The configured backend (Keychain, file, 1Password or Vault) stores the Codex profiles under `Codex-Account-<label>`.

- A local fallback copy uses the same key in the local backend.
- The registry file `codex-profiles.json` holds no secrets.

**Testing**: `go test ./...`. A fake `codex` script on `PATH` for sign-in tests. An `httptest` server for usage tests.

**Target Platform**: macOS (primary), Linux.

**Project Type**: CLI.

**Performance Goals**: the usage command finishes in under 5 seconds for all profiles.

- ccswitch queries the profiles at the same time.
- Each query has a 10-second limit.

**Constraints**: never print a token. Never renew a Codex token. Never change the live login during a sign-in. One autoswitch run at a time.

**Scale/Scope**: 2 to 5 Codex profiles.

## Constitution Check

`.specify/memory/constitution.md` is the unfilled template. It has no gates.

The user's global rules apply instead:

- Simplest working change.
- Tests with new behavior.
- No new dependency.
- Surgical edits.

This plan passes them. It adds no dependency. It reuses the backend, browser and lock code.

## Design

### D1. Identity-based snapshot (spec story 4, FR-008, FR-009)

- Add `codex.MatchProfile(reg, live) (label string, ok bool)`. It matches by account ID, and also by email when both have one.
- `snapshotLiveCodex` uses `MatchProfile`, not `reg.Active`. If no profile matches, `codex use` stops with an error and changes nothing.
- If the store write fails, write the same bytes to the local backend under the same key.
- Add `codexFresher(local, stored)`: it returns the copy with the later `last_refresh`. `codex use` and `codex usage` read through it.

### D2. Sign-in (spec story 1, FR-001 to FR-003)

- New command: `ccswitch codex login <email> [--label <label>]`. The default label is the email.
- Make a temporary directory (mode 0700). Run `codex login --device-auth` with `CODEX_HOME` set to that directory.
- Read stdout and stderr line by line. Find the `https://` address and the one-time code. Print the code to the user.
- Open the address in the Chrome profile from `browser.FindByEmail(email)`.
  - Add `browser.OpenURL(profileDir, url)`. It runs Chrome with `--profile-directory`.
  - If no profile matches, print the address. Tell the user to open it.
- If the output shows the browser sign-in (`Starting local login server`), tell the user that Codex opened the default browser.
- After `codex` exits with status 0, read `<tmp>/auth.json`.
  - Compare `codex.Describe(...).Email` with the requested email. The comparison ignores case.
  - On a match, save the login to the store. Add it to the registry. Add it to the end of the priority list.
  - On no match, save nothing. Report the email that signed in.
- Always delete the temporary directory. Never run `codex logout`.
- Use a lock file so two sign-ins cannot run at the same time.

### D3. Priority (FR-004)

- Add `Priority []string` to `codex.Registry` (JSON `priority`, omitted when empty). Labels missing from the list come after it, in label order.
- New command: `ccswitch codex priority <label>...` sets the order. With no arguments, it prints the order.

### D4. Usage (spec story 2, FR-005 to FR-007, FR-013)

- New package file `internal/codex/usage.go` with:
  - `FetchUsage(ctx, accessToken, accountID) (Usage, error)`.

  - The URL is `https://chatgpt.com/backend-api/wham/usage`. The environment variable `CCSWITCH_CODEX_USAGE_URL` overrides the URL for tests.

  - The types are `Usage{Windows []Window, Plan string}` and `Window{Seconds int, UsedPercent float64, ResetAt time.Time}`.

  - Both `primary_window` and `secondary_window` map to `Windows`. ccswitch skips a null window.

  - Status: `ok`, `stale` (access token `exp` has passed), `expired` (HTTP 401 or 403: sign in again), `unavailable` (network error, 429, 5xx).
- New command: `ccswitch codex usage [--json]`.
  - The live profile uses the live file. Other profiles use their saved copy.
  - ccswitch queries the profiles at the same time.
- The load of a profile is the highest `UsedPercent` of its windows.

### D5. Autoswitch (spec story 3, FR-010 to FR-012)

- New command: `ccswitch codex autoswitch --once [--threshold 80] [--cooldown 5m]`.
- Pure function `decideCodexSwitch(rows, liveLabel, priority, threshold, cooldownActive) (target, reason)`:
  - Live usage not `ok`: no action.
  - Cooldown active: no action.
  - Target is the first profile in priority order with status `ok` and load under the threshold.
  - No target, or target is the live profile: no action.
  - Otherwise, switch to the target.
- The switch calls the same code as `codex use`, so D1 applies.
- Lock file `codex-autoswitch.lock`, the same pattern as `lockAutoswitch`.
- Record the switch time in the registry (`lastSwitch`) for the cooldown.

### D6. Hook (FR-012)

- In the dotfiles repo, add a Codex `SessionStart` hook that runs `ccswitch codex autoswitch --once` in the background. Output goes to `~/.claude-switch-backup/codex-autoswitch.log` and `codex-autoswitch.error.log`. This copies the Claude hook in `claude.nix`.

## Project Structure

### Documentation (this feature)

```text
specs/001-codex-multi-account/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/cli.md
└── tasks.md
```

### Source Code (repository root)

```text
internal/codex/
├── auth.go          # add MatchProfile, AccessTokenExpiry, LastRefresh
├── registry.go      # add Priority, LastSwitch, Ordered()
├── usage.go         # new: FetchUsage, Usage, Window
├── login.go         # new: device sign-in runner and output parser
└── *_test.go
internal/browser/
└── chrome.go        # add OpenURL
internal/cli/
├── codex.go         # identity-based snapshot, local fallback, new subcommands
├── codex_usage.go   # new: codex usage
├── codex_autoswitch.go  # new: codex autoswitch + decideCodexSwitch
└── *_test.go
README.md            # Codex section
```

**Structure Decision**: the feature stays in the existing `internal/codex` and `internal/cli` packages. No new package.

## Complexity Tracking

No violations.
