#!/usr/bin/env bats
#
# CLI smoke tests for the ccswitch Go binary. These exercise the
# command surface end-to-end without touching real credentials: every
# test runs against an isolated $HOME so sequence.json, settings.json,
# and .claude.json are all sandboxed.
#
# Run with:  make smoke         (builds the binary, then runs this)
#        or:  CCSWITCH_BIN=/path/to/ccswitch bats tests/cli.bats

setup() {
    CCSWITCH="${CCSWITCH_BIN:-$BATS_TEST_DIRNAME/../bin/ccswitch}"
    [ -x "$CCSWITCH" ] || skip "ccswitch binary not found at $CCSWITCH (run: make build)"

    # Isolated HOME so the suite cannot read or mutate real state.
    export HOME="$BATS_TEST_TMPDIR/home"
    mkdir -p "$HOME/.claude" "$HOME/.claude-switch-backup"
    # Force the file backend — no keychain / 1Password / network.
    export CCSWITCH_BACKEND=file

    # A two-account sequence.json; activeAccountId deliberately disagrees
    # with .claude.json to exercise live-state resolution. Account IDs are
    # the real HashEmail (8-char SHA-256 prefix) of each email so that
    # activeID() can resolve the live .claude.json account.
    cat > "$HOME/.claude-switch-backup/sequence.json" <<'JSON'
{
  "version": 2,
  "activeAccountId": "00000000",
  "sequence": ["ff8d9819", "5ff860bf"],
  "accounts": {
    "ff8d9819": {"email": "alice@example.com", "orgName": "Alice's Organization"},
    "5ff860bf": {"email": "bob@example.com", "orgName": "Acme Inc"}
  }
}
JSON
    # .claude.json names alice as the live account.
    cat > "$HOME/.claude.json" <<'JSON'
{"oauthAccount": {"emailAddress": "alice@example.com", "organizationName": "Alice's Organization"}}
JSON
}

@test "help: exits 0 and lists subcommands" {
    run "$CCSWITCH" --help
    [ "$status" -eq 0 ]
    [[ "$output" == *"Usage:"* ]]
    [[ "$output" == *"switch-to"* ]]
}

@test "list: shows both managed accounts" {
    run "$CCSWITCH" list
    [ "$status" -eq 0 ]
    [[ "$output" == *"alice@example.com"* ]]
    [[ "$output" == *"bob@example.com"* ]]
}

@test "list: marks the live account (.claude.json) active, not the stale sequence field" {
    run "$CCSWITCH" list
    [ "$status" -eq 0 ]
    # alice is live in .claude.json; the (active) marker must be on her line.
    [[ "$output" == *"alice@example.com  [Personal] (active)"* ]]
    [[ "$output" != *"bob@example.com"*"(active)"* ]]
}

@test "list: empty org renders as Personal, real org passes through" {
    run "$CCSWITCH" list
    [[ "$output" == *"[Personal]"* ]]
    [[ "$output" == *"[Acme Inc]"* ]]
}

@test "current: reports the live account" {
    run "$CCSWITCH" current
    [ "$status" -eq 0 ]
    [[ "$output" == *"alice@example.com"* ]]
}

@test "legacy --list flag is rewritten to the list subcommand" {
    run "$CCSWITCH" --list
    [ "$status" -eq 0 ]
    [[ "$output" == *"alice@example.com"* ]]
}

@test "switch-to: unknown identifier fails loudly (not a silent exit 1)" {
    run "$CCSWITCH" switch-to 99
    [ "$status" -ne 0 ]
    [[ "$output" == *"no account found matching: 99"* ]]
}

@test "legacy --switch-to: unknown identifier also fails loudly" {
    run "$CCSWITCH" --switch-to 99
    [ "$status" -ne 0 ]
    [[ "$output" == *"no account found matching: 99"* ]]
}

@test "set-limit: targets the live active account, not the stale sequence field" {
    run "$CCSWITCH" set-limit 6700M
    [ "$status" -eq 0 ]
    [[ "$output" == *"alice@example.com"* ]]
    [[ "$output" == *"6,700,000,000"* ]]
    # Persisted against alice (ff8d9819), never the stale 00000000.
    grep -q '"weeklyTokenLimit": 6700000000' "$HOME/.claude-switch-backup/sequence.json"
    run grep -A2 '"5ff860bf"' "$HOME/.claude-switch-backup/sequence.json"
    [[ "$output" != *"weeklyTokenLimit"* ]]
}

@test "set-limit: rejects a malformed limit" {
    run "$CCSWITCH" set-limit not-a-number
    [ "$status" -ne 0 ]
}

@test "api-status: default Anthropic API when no settings.json" {
    run "$CCSWITCH" api-status
    [ "$status" -eq 0 ]
    [[ "$output" == *"Default Anthropic API"* ]]
}

@test "use-anthropic: no-op when already on the default API" {
    run "$CCSWITCH" use-anthropic
    [ "$status" -eq 0 ]
    [[ "$output" == *"default Anthropic API"* ]]
}

@test "api-status: reports a custom endpoint after settings.json is configured" {
    cat > "$HOME/.claude/settings.json" <<'JSON'
{"env": {"ANTHROPIC_BASE_URL": "https://api.z.ai/api/anthropic", "API_TIMEOUT_MS": "3000000"}}
JSON
    run "$CCSWITCH" api-status
    [ "$status" -eq 0 ]
    [[ "$output" == *"Custom endpoint"* ]]
    [[ "$output" == *"z.ai"* ]]
}

@test "use-anthropic: strips the custom env block" {
    cat > "$HOME/.claude/settings.json" <<'JSON'
{"env": {"ANTHROPIC_BASE_URL": "https://api.z.ai/api/anthropic"}, "other": "kept"}
JSON
    run "$CCSWITCH" use-anthropic
    [ "$status" -eq 0 ]
    [[ "$output" == *"Reverted"* ]]
    run grep -c "ANTHROPIC_BASE_URL" "$HOME/.claude/settings.json"
    [[ "$output" == "0" ]]
    # Unrelated keys survive.
    grep -q '"other": "kept"' "$HOME/.claude/settings.json"
}

@test "no accounts: list reports an empty state cleanly" {
    rm "$HOME/.claude-switch-backup/sequence.json"
    run "$CCSWITCH" list
    [ "$status" -eq 0 ]
    [[ "$output" == *"No accounts are managed yet"* ]]
}
