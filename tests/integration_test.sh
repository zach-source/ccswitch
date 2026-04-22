#!/usr/bin/env bash
# Integration tests for ccswitch credential storage.
#
# Exercises round-trip read/write for each credential backend to catch
# regressions in the backend plumbing. Each test runs in an isolated
# $HOME so it cannot affect real credentials. Backends whose
# prerequisites are missing are skipped, not failed.
#
# Run with: bash tests/integration_test.sh
# Set TEST_VERBOSE=1 for verbose output.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CCSWITCH="$SCRIPT_DIR/../ccswitch.sh"

if [[ ! -f "$CCSWITCH" ]]; then
    echo "ERROR: cannot find ccswitch.sh at $CCSWITCH" >&2
    exit 2
fi

# ccswitch requires bash 4.4+ (associative arrays, process substitution, etc.).
# macOS /bin/bash is 3.2, so find a modern bash on PATH (nix/homebrew) and
# use it explicitly inside the test harness.
TEST_BASH=""
for candidate in "$(command -v bash)" /opt/homebrew/bin/bash /usr/local/bin/bash; do
    [[ -x "$candidate" ]] || continue
    if "$candidate" -c 'exit $(( ${BASH_VERSINFO[0]} >= 4 ? 0 : 1 ))' 2>/dev/null; then
        TEST_BASH="$candidate"
        break
    fi
done
if [[ -z "$TEST_BASH" ]]; then
    echo "ERROR: no bash >= 4 on PATH — tests need modern bash for namerefs/etc." >&2
    exit 2
fi

# ccswitch's TOML parser uses Python stdlib `tomllib` (Python 3.11+) with a
# tomli fallback. macOS /usr/bin/python3 is 3.9 and lacks both. Find a
# python3 with tomllib so the TOML-reading tests actually read the config.
TEST_PYTHON=""
for candidate in "$(command -v python3)" /opt/homebrew/bin/python3 /usr/local/bin/python3 /usr/bin/python3; do
    [[ -x "$candidate" ]] || continue
    if "$candidate" -c 'import tomllib' 2>/dev/null \
       || "$candidate" -c 'import tomli' 2>/dev/null; then
        TEST_PYTHON="$candidate"
        break
    fi
done
if [[ -z "$TEST_PYTHON" ]]; then
    echo "ERROR: no python3 with tomllib/tomli — TOML-loading tests will silently skip config." >&2
    exit 2
fi

# Build a PATH for test invocations: explicit bash/python dirs, plus the dirs
# of every other tool ccswitch shells out to (jq, op, security) so tests work
# even inside an `env -i` sandbox that strips the caller's PATH.
_test_tool_dir() { local t="$1"; command -v "$t" 2>/dev/null | xargs -I{} dirname {} 2>/dev/null; }
TEST_PATH_PARTS=(
    "$(dirname "$TEST_BASH")"
    "$(dirname "$TEST_PYTHON")"
    "$(_test_tool_dir jq)"
    "$(_test_tool_dir op)"
    "$(_test_tool_dir security)"
    "/usr/bin" "/bin" "/usr/sbin" "/sbin"
)
TEST_PATH=""
# De-dup while preserving order
for p in "${TEST_PATH_PARTS[@]}"; do
    [[ -z "$p" ]] && continue
    case ":$TEST_PATH:" in *":$p:"*) ;; *) TEST_PATH="${TEST_PATH:+$TEST_PATH:}$p" ;; esac
done

# ────────────────────────────────────────────────────────────────────────────
# Test harness
# ────────────────────────────────────────────────────────────────────────────
TEST_ROOT="$(mktemp -d -t ccswitch-test.XXXXXX)"
trap 'rm -rf "$TEST_ROOT"' EXIT

pass_count=0
fail_count=0
skip_count=0

log()  { echo "[test] $*"; }
pass() { echo "  \033[32mPASS\033[0m: $*" | sed 's/\\033/\x1b/g'; pass_count=$((pass_count+1)); }
fail() { echo "  \033[31mFAIL\033[0m: $*" | sed 's/\\033/\x1b/g'; fail_count=$((fail_count+1)); }
skip() { echo "  \033[33mSKIP\033[0m: $*" | sed 's/\\033/\x1b/g'; skip_count=$((skip_count+1)); }

# Fake credential blob — well-formed JSON mirroring Claude Code's shape.
FAKE_CRED='{"claudeAiOauth":{"accessToken":"sk-ant-oat01-TESTACCESS","refreshToken":"sk-ant-ort01-TESTREFRESH","expiresAt":9999999999999,"scopes":["user:inference"],"subscriptionType":"test","rateLimitTier":"test"}}'
FAKE_CRED_2='{"claudeAiOauth":{"accessToken":"sk-ant-oat01-TESTACCESS2","refreshToken":"sk-ant-ort01-TESTREFRESH2","expiresAt":9999999999998,"scopes":["user:inference"],"subscriptionType":"test","rateLimitTier":"test"}}'

# Run a bash sub-process with ccswitch sourced and a test body evaluated.
# Last argument is the test body. Preceding arguments are env var assignments
# of the form NAME=VALUE (quoted values with spaces handled correctly because
# each is a single shell word).
run_with() {
    local body="${!#}"
    local -a env_args=("${@:1:$#-1}")
    # env -i wipes the environment; we then re-add only PATH/TERM plus the
    # test-specified vars. Each env_args element is one KEY=VALUE word, so
    # values with spaces (e.g. "Personal Agents") survive intact.
    env -i \
        PATH="$TEST_PATH" \
        TERM="${TERM:-dumb}" \
        "${env_args[@]}" \
        "$TEST_BASH" --noprofile --norc -c "source '$CCSWITCH' >/dev/null 2>&1; $body"
}

# Like run_with but inherits the caller's environment — use this when the
# test needs access to macOS session credentials (login keychain, etc.)
# that are tied to the user's auditable session, which `env -i` strips out.
run_with_inherited() {
    local body="${!#}"
    local -a env_args=("${@:1:$#-1}")
    env "${env_args[@]}" "$TEST_BASH" --noprofile --norc -c "source '$CCSWITCH' >/dev/null 2>&1; $body"
}

# ────────────────────────────────────────────────────────────────────────────
# File backend: portable, no external deps
# ────────────────────────────────────────────────────────────────────────────
test_file_backend() {
    log "== file backend =="
    local home="$TEST_ROOT/file-backend"
    mkdir -p "$home/.claude"

    # Round-trip write/read active creds
    if run_with "HOME=$home" "CCSWITCH_BACKEND=file" "CCSWITCH_CONFIG_FILE=/dev/null" "
        write_credentials '$FAKE_CRED'
        got=\$(read_credentials)
        [[ \"\$got\" == '$FAKE_CRED' ]]
    "; then
        pass "file: active credentials round-trip"
    else
        fail "file: active credentials round-trip"
    fi

    # Verify file was written with 0600 perms
    local cred_file="$home/.claude/.credentials.json"
    if [[ -f "$cred_file" ]]; then
        local perms
        perms=$(/usr/bin/stat -f '%Lp' "$cred_file" 2>/dev/null || stat -c '%a' "$cred_file" 2>/dev/null)
        if [[ "$perms" == "600" ]]; then
            pass "file: credentials.json has 0600 perms"
        else
            fail "file: expected 0600 perms, got $perms"
        fi
    else
        fail "file: credentials.json not created"
    fi

    # Per-account round-trip
    mkdir -p "$home/.claude-switch-backup/credentials"
    if run_with "HOME=$home" "CCSWITCH_BACKEND=file" "CCSWITCH_CONFIG_FILE=/dev/null" "
        write_account_credentials 'abcd1234' 'test@example.com' '$FAKE_CRED_2'
        got=\$(read_account_credentials 'abcd1234' 'test@example.com')
        [[ \"\$got\" == '$FAKE_CRED_2' ]]
    "; then
        pass "file: per-account round-trip"
    else
        fail "file: per-account round-trip"
    fi

    # Delete
    if run_with "HOME=$home" "CCSWITCH_BACKEND=file" "CCSWITCH_CONFIG_FILE=/dev/null" "
        delete_account_credentials 'abcd1234' 'test@example.com'
        got=\$(read_account_credentials 'abcd1234' 'test@example.com')
        [[ -z \"\$got\" ]]
    "; then
        pass "file: delete per-account"
    else
        fail "file: delete per-account"
    fi

    # Empty read returns empty (not garbage)
    rm -f "$cred_file"
    if run_with "HOME=$home" "CCSWITCH_BACKEND=file" "CCSWITCH_CONFIG_FILE=/dev/null" "
        got=\$(read_credentials)
        [[ -z \"\$got\" ]]
    "; then
        pass "file: missing credentials returns empty"
    else
        fail "file: missing credentials returns empty"
    fi
}

# ────────────────────────────────────────────────────────────────────────────
# Keychain backend (macOS only)
# ────────────────────────────────────────────────────────────────────────────
test_keychain_backend() {
    log "== keychain backend =="
    if [[ "$(uname -s)" != "Darwin" ]]; then
        skip "keychain: not on macOS"
        return
    fi
    if ! command -v security &>/dev/null; then
        skip "keychain: security CLI not found"
        return
    fi

    # Unique service name to avoid collision with real creds
    local svc="ccswitch-test-$$-$RANDOM"

    # Cleanup helper
    cleanup_keychain() {
        security delete-generic-password -s "$svc" &>/dev/null || true
        security delete-generic-password -s "${svc}-acct" &>/dev/null || true
    }
    cleanup_keychain

    # Keychain ACLs are bound to the user's login session; `env -i` strips
    # the session identifiers and `security add-generic-password` then
    # fails with "authorization canceled". Inherit env for this test only.
    if run_with_inherited "CCSWITCH_BACKEND=keychain" "CCSWITCH_CONFIG_FILE=/dev/null" "
        _keychain_write '$svc' '$FAKE_CRED'
        got=\$(_keychain_read '$svc')
        [[ \"\$got\" == '$FAKE_CRED' ]]
    "; then
        pass "keychain: round-trip"
    else
        fail "keychain: round-trip"
    fi

    # Delete
    if run_with_inherited "CCSWITCH_BACKEND=keychain" "CCSWITCH_CONFIG_FILE=/dev/null" "
        _keychain_delete '$svc'
        got=\$(_keychain_read '$svc')
        [[ -z \"\$got\" ]]
    "; then
        pass "keychain: delete"
    else
        fail "keychain: delete"
    fi

    cleanup_keychain
}

# ────────────────────────────────────────────────────────────────────────────
# 1Password backend — only runs when OP is authed or Connect is configured.
# Uses a unique prefix so tests never touch real ccswitch items.
# ────────────────────────────────────────────────────────────────────────────
test_1password_backend() {
    log "== 1password backend =="

    if ! command -v op &>/dev/null; then
        skip "1password: op CLI not installed"
        return
    fi

    # Pick up user's configured vault + account if present (so tests target
    # the same place as the real install) but never touch real items.
    local vault="${CCSWITCH_OP_VAULT:-}"
    local op_account="${CCSWITCH_OP_ACCOUNT:-}"
    local connect_host="${OP_CONNECT_HOST:-${CCSWITCH_OP_CONNECT_HOST:-}}"

    # If the user's TOML exists, pull the vault from it via `ccswitch --config`
    if [[ -z "$vault" ]] && [[ -f "${HOME}/.config/ccswitch/config.toml" ]]; then
        vault=$(grep -E '^\s*vault\s*=' "${HOME}/.config/ccswitch/config.toml" 2>/dev/null | head -1 | sed -E 's/^\s*vault\s*=\s*"?([^"]*)"?\s*$/\1/')
    fi
    vault="${vault:-Private}"

    # Build op args to probe
    local probe_args=()
    if [[ -n "$connect_host" ]] && [[ -n "${OP_CONNECT_TOKEN:-}" ]]; then
        log "  (Connect mode via $connect_host)"
    elif [[ -n "$op_account" ]]; then
        probe_args+=(--account "$op_account")
        log "  (signed-in mode, account=$op_account)"
    else
        log "  (signed-in mode, default account)"
    fi

    if ! op vault list "${probe_args[@]}" --format=json &>/dev/null; then
        skip "1password: op not authed / Connect unreachable"
        return
    fi

    local prefix="ccswitch-test-$$-$RANDOM"

    cleanup_1password() {
        op item delete "${prefix}-active" "${probe_args[@]}" --vault "$vault" &>/dev/null || true
        op item delete "${prefix}-abcd1234-test@example.com" "${probe_args[@]}" --vault "$vault" &>/dev/null || true
    }
    cleanup_1password
    trap cleanup_1password EXIT

    # Build env-arg array (each element is one shell word — handles spaces)
    local -a env_args=(
        "HOME=$HOME"
        "CCSWITCH_BACKEND=1password"
        "CCSWITCH_CONFIG_FILE=/dev/null"
        "CCSWITCH_OP_VAULT=$vault"
        "CCSWITCH_OP_ITEM_PREFIX=$prefix"
    )
    [[ -n "$op_account" ]] && env_args+=("CCSWITCH_OP_ACCOUNT=$op_account")
    [[ -n "$connect_host" ]] && env_args+=("OP_CONNECT_HOST=$connect_host")
    [[ -n "${OP_CONNECT_TOKEN:-}" ]] && env_args+=("OP_CONNECT_TOKEN=$OP_CONNECT_TOKEN")

    if run_with "${env_args[@]}" "
        write_credentials '$FAKE_CRED'
        got=\$(read_credentials)
        [[ \"\$got\" == '$FAKE_CRED' ]]
    "; then
        pass "1password: active round-trip (vault=$vault)"
    else
        fail "1password: active round-trip (vault=$vault)"
    fi

    if run_with "${env_args[@]}" "
        write_account_credentials 'abcd1234' 'test@example.com' '$FAKE_CRED_2'
        got=\$(read_account_credentials 'abcd1234' 'test@example.com')
        [[ \"\$got\" == '$FAKE_CRED_2' ]]
    "; then
        pass "1password: per-account round-trip"
    else
        fail "1password: per-account round-trip"
    fi

    cleanup_1password
    trap 'rm -rf "$TEST_ROOT"' EXIT
}

# ────────────────────────────────────────────────────────────────────────────
# Backend resolution: verify CCSWITCH_BACKEND+TOML path mapping
# ────────────────────────────────────────────────────────────────────────────
test_backend_resolution() {
    log "== backend resolution =="

    # TOML-configured backend
    local cfg="$TEST_ROOT/resolve-toml.toml"
    cat > "$cfg" <<'EOF'
[backend]
type = "file"
EOF
    if run_with "HOME=$TEST_ROOT" "CCSWITCH_CONFIG_FILE=$cfg" "
        got=\$(_resolve_backend)
        [[ \"\$got\" == 'file' ]]
    "; then
        pass "resolve: TOML type=file"
    else
        fail "resolve: TOML type=file"
    fi

    # Env var overrides TOML
    if run_with "HOME=$TEST_ROOT" "CCSWITCH_BACKEND=keychain" "CCSWITCH_CONFIG_FILE=$cfg" "
        got=\$(_resolve_backend)
        [[ \"\$got\" == 'keychain' ]]
    "; then
        pass "resolve: env CCSWITCH_BACKEND overrides TOML"
    else
        fail "resolve: env CCSWITCH_BACKEND overrides TOML"
    fi

    # Auto on macOS → keychain
    if [[ "$(uname -s)" == "Darwin" ]]; then
        if run_with "HOME=$TEST_ROOT" "CCSWITCH_BACKEND=auto" "CCSWITCH_CONFIG_FILE=/dev/null" "
            got=\$(_resolve_backend)
            [[ \"\$got\" == 'keychain' ]]
        "; then
            pass "resolve: auto → keychain on macOS"
        else
            fail "resolve: auto → keychain on macOS"
        fi
    fi
}

# ────────────────────────────────────────────────────────────────────────────
# Config loading: TOML → env var mapping
# ────────────────────────────────────────────────────────────────────────────
test_config_loading() {
    log "== config loading =="
    local cfg="$TEST_ROOT/config-load.toml"
    cat > "$cfg" <<'EOF'
[backend]
type = "1password"

[backend.onepassword]
vault = "MyVault"
item_prefix = "Custom Prefix"
account = "my.1password.com"
connect_host = "http://localhost:8080"

[sync]
interval = 600

[refresh]
expiry_buffer_minutes = 10
EOF

    if run_with "HOME=$TEST_ROOT" "CCSWITCH_CONFIG_FILE=$cfg" "
        [[ \"\$CCSWITCH_BACKEND\" == '1password' ]] \
            && [[ \"\$CCSWITCH_OP_VAULT\" == 'MyVault' ]] \
            && [[ \"\$CCSWITCH_OP_ITEM_PREFIX\" == 'Custom Prefix' ]] \
            && [[ \"\$CCSWITCH_OP_ACCOUNT\" == 'my.1password.com' ]] \
            && [[ \"\$CCSWITCH_OP_CONNECT_HOST\" == 'http://localhost:8080' ]] \
            && [[ \"\$CCSWITCH_SYNC_INTERVAL\" == '600' ]] \
            && [[ \"\$CCSWITCH_EXPIRY_BUFFER_MINUTES\" == '10' ]]
    "; then
        pass "config: TOML → env var mapping"
    else
        fail "config: TOML → env var mapping"
    fi
}

# ────────────────────────────────────────────────────────────────────────────
# Main
# ────────────────────────────────────────────────────────────────────────────
echo ""
log "ccswitch integration tests"
log "test root: $TEST_ROOT"
echo ""

test_config_loading
echo ""
test_backend_resolution
echo ""
test_file_backend
echo ""
test_keychain_backend
echo ""
test_1password_backend

echo ""
printf 'Results: %d passed, %d failed, %d skipped\n' "$pass_count" "$fail_count" "$skip_count"
[[ $fail_count -eq 0 ]] || exit 1
