# Quickstart: Validate Codex Multi-Account Switching

## Prerequisites

- Codex CLI 0.160.1 or later on `PATH`.
- One Chrome profile signed in to `ztaylor@stigen.ai`, and one signed in to `zctaylor.work@gmail.com`.
- A build of this branch: `go build -o /tmp/ccswitch ./cmd/ccswitch`.

## 1. Automated tests

```bash
go test ./internal/codex/... ./internal/browser/... ./internal/cli/...
```

Expected: all tests pass. The tests use a fake `codex` script and a local HTTP server. They do not use the network.

## 2. Sign in both accounts (story 1)

```bash
cp ~/.codex/auth.json /tmp/auth.before            # private copy, mode 600
/tmp/ccswitch codex login ztaylor@stigen.ai
/tmp/ccswitch codex login zctaylor.work@gmail.com
cmp ~/.codex/auth.json /tmp/auth.before && echo "live login unchanged"
rm /tmp/auth.before
/tmp/ccswitch codex priority                        # expect stigen.ai first
```

Expected: each sign-in page opens in the matching Chrome profile. Both profiles save. The live login does not change.

## 3. Usage (story 2)

```bash
/tmp/ccswitch codex usage
```

Expected: two rows in under 5 seconds. Each row shows the windows that the account reports.

## 4. Autoswitch (story 3)

```bash
/tmp/ccswitch codex autoswitch --once --threshold 1     # force: primary is over 1%
/tmp/ccswitch codex current                              # expect the secondary account
/tmp/ccswitch codex autoswitch --once --cooldown 0      # threshold 80 again
/tmp/ccswitch codex current                              # expect the primary account
```

## 5. Identity-safe swap (story 4)

Covered by the automated test that puts account B's login live while `active` names account A. Expected: account B's profile gets the live login, and account A's profile does not change.

## 6. Hook

Start a new Codex session. Then check `~/.claude-switch-backup/codex-autoswitch.log` for one new line.
