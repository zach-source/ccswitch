# Tasks: Codex Multi-Account Switching

**Input**: Design documents from `specs/001-codex-multi-account/`

**Prerequisites**: [plan.md](plan.md), [spec.md](spec.md), [research.md](research.md), [data-model.md](data-model.md), [contracts/cli.md](contracts/cli.md)

**Tests**: The user's rules require tests with new behavior, and TDD for fixes. Each task includes its tests. Write the test first and see it fail.

**Story map**: US1 = sign-in (P1). US2 = usage (P2). US3 = autoswitch (P3). US4 = safe swap (P1).

## Phase 1: Setup

No setup tasks. The project, the build and the test tools exist.

## Phase 2: Foundational (blocks all stories)

- [ ] T001 [P] Add `Priority`, `LastSwitch` and `Ordered()` to `Registry`, with tests for order, unknown labels and JSON round trip, in internal/codex/registry.go and internal/codex/registry_test.go
- [ ] T002 [P] Add `MatchProfile(reg, live)`, `AccessTokenExpiry(blob)` and `LastRefresh(blob)`, with tests from fake JWTs, in internal/codex/auth.go and internal/codex/auth_test.go

**Checkpoint**: `go test ./internal/codex/...` passes.

## Phase 3: User Story 4 – Never lose a rotated Codex login (P1)

**Goal**: a swap saves the live login into the profile with the same account, and never loses it.

**Independent test**: live login of account B while `active` names A. After `codex use`, B's profile holds the live bytes and A's profile does not change.

- [ ] T003 [US4] Write failing tests: live login of B with `active` = A, and a live login that matches no profile, in internal/cli/codex_test.go
- [ ] T004 [US4] Make `snapshotLiveCodex` use `MatchProfile`, and make `codex use` stop with no change when no profile matches, in internal/cli/codex.go
- [ ] T005 [US4] On a failed store write, write a local copy under the same key. Read through `codexFresher` (later `last_refresh` wins). Add tests with a failing store, in internal/cli/codex.go and internal/cli/codex_test.go

**Checkpoint**: US4 tests pass. `codex use` is safe for the stories below.

## Phase 4: User Story 1 – Sign in each account in its own browser profile (P1)

**Goal**: `ccswitch codex login <email>` signs in, in the correct Chrome profile, and saves the profile.

**Independent test**: a fake `codex` script prints device sign-in output and writes `auth.json` into `CODEX_HOME`. The test checks the browser call, the saved profile and that the live file did not change.

- [ ] T006 [P] [US1] Add `OpenURL(profileDir, url)` that runs Chrome with `--profile-directory`, with a test through a fake Chrome binary, in internal/browser/chrome.go and internal/browser/chrome_test.go
- [ ] T007 [P] [US1] Add the device sign-in output parser (address, one-time code, browser fallback line), with tests from the text in `device_code_auth.rs:149-158` and `cli/src/login.rs:116-119`, in internal/codex/login.go and internal/codex/login_test.go
- [ ] T008 [US1] Add the sign-in runner: temporary `CODEX_HOME` (0700), run `codex login --device-auth`, stream output, open the address, lock file, always delete the directory, never run `codex logout`, in internal/codex/login.go
- [ ] T009 [US1] Add `ccswitch codex login <email> [--label]`: email check, save to store, registry entry, add to end of priority. Test with a fake `codex` on `PATH` for match, mismatch and failure, in internal/cli/codex.go and internal/cli/codex_test.go
- [ ] T010 [US1] Add `ccswitch codex priority [<label>...]`, and remove the label from priority in `codex remove`, with tests, in internal/cli/codex.go and internal/cli/codex_test.go

**Checkpoint**: both accounts can sign in. MVP with US4.

## Phase 5: User Story 2 – See usage for every account (P2)

**Goal**: `ccswitch codex usage` shows each profile's windows.

**Independent test**: an `httptest` server returns weekly-only, 5h-plus-weekly, 401, 429 and 500 responses. A profile with an expired access token shows "stale" and makes no request.

- [ ] T011 [P] [US2] Add `FetchUsage`, `Usage`, `Window`, the window mapping by `limit_window_seconds`, the status rules and the `CCSWITCH_CODEX_USAGE_URL` override, with `httptest` tests, in internal/codex/usage.go and internal/codex/usage_test.go
- [ ] T012 [US2] Add `ccswitch codex usage [--json]`: live profile from the live file, others from `codexFresher`, queries at the same time with a 10-second limit, text and JSON output per contracts/cli.md, in internal/cli/codex_usage.go and internal/cli/codex_usage_test.go

**Checkpoint**: usage shows both accounts in under 5 seconds.

## Phase 6: User Story 3 – Switch by priority (P3)

**Goal**: the next session uses the highest-priority account under the threshold.

**Independent test**: table tests of `decideCodexSwitch`. A command test with fake usage switches the live file once and records `lastSwitch`.

- [ ] T013 [US3] Add the pure `decideCodexSwitch(rows, liveLabel, priority, threshold, cooldownActive)` with table tests for every reason in contracts/cli.md, in internal/cli/codex_autoswitch.go and internal/cli/codex_autoswitch_test.go
- [ ] T014 [US3] Add `ccswitch codex autoswitch --once [--threshold] [--cooldown]`: lock file `codex-autoswitch.lock`, cooldown from `lastSwitch`, switch through the `codex use` path, in internal/cli/codex_autoswitch.go
- [ ] T015 [US3] Add the Codex `SessionStart` hook that runs `ccswitch codex autoswitch --once` in the background, with logs in `~/.claude-switch-backup/codex-autoswitch.log`, in the dotfiles repo (`~/dotfiles/nix`, Codex hooks config)

**Checkpoint**: quickstart step 4 passes.

## Phase 7: Polish

- [ ] T016 [P] Update the Codex section and the command list in README.md
- [ ] T017 Run the manual checks in quickstart.md with the two real accounts. Record whether the ChatGPT app shares `~/.codex/auth.json` and whether `stigen.ai` allows device sign-in
- [ ] T018 Release v2.11.0 (bump `version` in flake.nix, annotated tag) and bump the ccswitch input in the dotfiles flake

## Dependencies

```text
T001, T002 ──► US4 (T003 → T004 → T005)
T001, T002 ──► US1 (T006 ∥ T007 → T008 → T009 → T010)
T002       ──► US2 (T011 → T012)
US4 + US2 + T010 ──► US3 (T013 → T014 → T015)
All stories ──► T016, T017 → T018
```

- US4 must finish before US3, because autoswitch uses the `codex use` path.
- US1 and US2 can run at the same time after Phase 2.

## Parallel examples

- Phase 2: T001 and T002 (different files).
- US1: T006 and T007 (different packages).
- After Phase 2: US1, US2 and US4 in separate worktrees.

## Implementation strategy

1. MVP: Phase 2, then US4, then US1. The user can sign in both accounts and swap them safely by hand.
2. Add US2, then US3 and the hook.
3. Polish and release.
