#!/usr/bin/env bash

# Multi-Account Switcher for Claude Code
# Simple tool to manage and switch between multiple Claude Code accounts

set -euo pipefail

# Configuration
readonly BACKUP_DIR="$HOME/.claude-switch-backup"
readonly SEQUENCE_FILE="$BACKUP_DIR/sequence.json"
readonly SCHEMA_VERSION=2

# Config file location (override via CCSWITCH_CONFIG_FILE)
readonly CCSWITCH_CONFIG_FILE_DEFAULT="$HOME/.config/ccswitch/config.toml"
CCSWITCH_CONFIG_FILE="${CCSWITCH_CONFIG_FILE:-$CCSWITCH_CONFIG_FILE_DEFAULT}"

# Keychain service names for Connect/CF Access secrets. Used in both the load
# path (_op_load_connect_token) and the setup command (cmd_setup_op_connect)
# — keep them as constants so renames don't desync silently.
readonly CCSWITCH_KC_CF_ID_SVC="ccswitch-cf-access-client-id"
readonly CCSWITCH_KC_CF_SECRET_SVC="ccswitch-cf-access-client-secret"

# Field label used inside 1Password Secure Note items for ccswitch credentials.
readonly CCSWITCH_OP_CRED_FIELD="credentials"

# Compute stable 8-char hex ID from email (SHA-256 prefix)
hash_email() {
    local email="$1"
    echo -n "$email" | shasum -a 256 | cut -c1-8
}

# ═══════════════════════════════════════════════════════════════════════════
# Config Loading: TOML + env var overrides
# ═══════════════════════════════════════════════════════════════════════════
# Precedence (highest wins): env var > TOML config > built-in default
# TOML schema:
#   [backend]
#   type = "auto" | "keychain" | "file" | "1password" | "vault"
#
#   [backend.onepassword]
#   vault = "Private"
#   item_prefix = "Claude Code Account"
#   account = ""          # optional op --account shorthand
#
#   [backend.vault]
#   addr = "https://vault.example.com"
#   path = "secret/data/ccswitch"
#   token = ""            # or inherit VAULT_TOKEN
#
#   [sync]
#   interval = 300        # daemon tick in seconds
#
#   [refresh]
#   expiry_buffer_minutes = 5
_load_config() {
    # Parse TOML config if it exists and emit `KEY=value` lines.
    # Env vars set before this function runs are NOT overridden.
    local config_output=""
    if [[ -f "$CCSWITCH_CONFIG_FILE" ]]; then
        config_output=$(python3 -c "
import sys, os
try:
    import tomllib
except ImportError:
    try:
        import tomli as tomllib
    except ImportError:
        sys.exit(0)  # no TOML parser, silently skip

try:
    with open('$CCSWITCH_CONFIG_FILE', 'rb') as f:
        cfg = tomllib.load(f)
except Exception as e:
    print(f'# Error parsing config: {e}', file=sys.stderr)
    sys.exit(0)

# Map TOML paths to env var names
mapping = {
    ('backend', 'type'): 'CCSWITCH_BACKEND',
    ('backend', 'onepassword', 'vault'): 'CCSWITCH_OP_VAULT',
    ('backend', 'onepassword', 'item_prefix'): 'CCSWITCH_OP_ITEM_PREFIX',
    ('backend', 'onepassword', 'account'): 'CCSWITCH_OP_ACCOUNT',
    ('backend', 'onepassword', 'connect_host'): 'CCSWITCH_OP_CONNECT_HOST',
    ('backend', 'onepassword', 'connect_token_keychain_service'): 'CCSWITCH_OP_CONNECT_TOKEN_KEYCHAIN_SERVICE',
    ('backend', 'onepassword', 'connect_token_keychain_account'): 'CCSWITCH_OP_CONNECT_TOKEN_KEYCHAIN_ACCOUNT',
    ('backend', 'vault', 'addr'): 'CCSWITCH_VAULT_ADDR',
    ('backend', 'vault', 'path'): 'CCSWITCH_VAULT_PATH',
    ('backend', 'vault', 'token'): 'CCSWITCH_VAULT_TOKEN',
    ('sync', 'interval'): 'CCSWITCH_SYNC_INTERVAL',
    ('refresh', 'expiry_buffer_minutes'): 'CCSWITCH_EXPIRY_BUFFER_MINUTES',
}

def dig(d, path):
    for k in path:
        if not isinstance(d, dict) or k not in d:
            return None
        d = d[k]
    return d

for path, var in mapping.items():
    val = dig(cfg, path)
    if val is not None:
        # Shell-escape the value (single quotes, escape single quotes inside)
        s = str(val).replace(\"'\", \"'\\\\''\")
        print(f\"{var}='{s}'\")
" 2>/dev/null)
    fi

    # Apply each config line only if the env var is not already set
    if [[ -n "$config_output" ]]; then
        while IFS= read -r line; do
            [[ -z "$line" ]] && continue
            [[ "$line" == \#* ]] && continue
            local var_name="${line%%=*}"
            # If the var is already set in environment, skip (env wins)
            if [[ -z "${!var_name:-}" ]]; then
                eval "export $line"
            fi
        done <<< "$config_output"
    fi

    # Apply built-in defaults for anything still unset
    : "${CCSWITCH_BACKEND:=auto}"
    : "${CCSWITCH_OP_VAULT:=Private}"
    : "${CCSWITCH_OP_ITEM_PREFIX:=Claude Code Account}"
    : "${CCSWITCH_OP_ACCOUNT:=}"
    : "${CCSWITCH_OP_CONNECT_HOST:=${OP_CONNECT_HOST:-}}"
    : "${CCSWITCH_OP_CONNECT_TOKEN_KEYCHAIN_SERVICE:=ccswitch-op-connect-token}"
    : "${CCSWITCH_OP_CONNECT_TOKEN_KEYCHAIN_ACCOUNT:=ccswitch}"
    : "${CCSWITCH_VAULT_ADDR:=${VAULT_ADDR:-}}"
    : "${CCSWITCH_VAULT_PATH:=secret/data/ccswitch}"
    : "${CCSWITCH_VAULT_TOKEN:=${VAULT_TOKEN:-}}"
    : "${CCSWITCH_SYNC_INTERVAL:=300}"
    : "${CCSWITCH_EXPIRY_BUFFER_MINUTES:=5}"
    export CCSWITCH_BACKEND CCSWITCH_OP_VAULT CCSWITCH_OP_ITEM_PREFIX CCSWITCH_OP_ACCOUNT
    export CCSWITCH_OP_CONNECT_HOST CCSWITCH_OP_CONNECT_TOKEN_KEYCHAIN_SERVICE CCSWITCH_OP_CONNECT_TOKEN_KEYCHAIN_ACCOUNT
    export CCSWITCH_VAULT_ADDR CCSWITCH_VAULT_PATH CCSWITCH_VAULT_TOKEN
    export CCSWITCH_SYNC_INTERVAL CCSWITCH_EXPIRY_BUFFER_MINUTES

    # Reset Connect lookup caches on reload so a reconfigure
    # (cmd_setup_op_connect calls _load_config after writing TOML) doesn't
    # keep stale ids.
    _OP_CONNECT_CACHED_VAULT_ID=""
    _OP_CONNECT_ITEM_ID_CACHE=()

    # Propagate Connect mode to op CLI env vars that the CLI recognizes natively.
    # Token load failure is non-fatal — _op_check surfaces it on demand.
    if [[ -n "$CCSWITCH_OP_CONNECT_HOST" ]]; then
        export OP_CONNECT_HOST="$CCSWITCH_OP_CONNECT_HOST"
        _op_load_connect_token || true
    fi
}

# Generic keychain-or-file token loader. Reads a secret by (service, account)
# from macOS Keychain, falling back to ~/.config/ccswitch/<file> on non-macOS
# or when the keychain lookup misses. Caller assigns the result to a var.
# Args: <service> <account> <file-basename>
_load_secret() {
    local service="$1" account="$2" file="$3"
    if command -v security &>/dev/null; then
        local tok
        tok=$(security find-generic-password -a "$account" -s "$service" -w 2>/dev/null || true)
        [[ -n "$tok" ]] && { printf '%s' "$tok"; return 0; }
    fi
    local token_file="${HOME}/.config/ccswitch/${file}"
    if [[ -f "$token_file" ]]; then
        cat "$token_file"
        return 0
    fi
    return 1
}

# Load all Connect secrets into env vars the HTTP layer reads:
#   OP_CONNECT_TOKEN         — X-OP-Token header
#   CF_ACCESS_CLIENT_ID      — CF-Access-Client-Id header
#   CF_ACCESS_CLIENT_SECRET  — CF-Access-Client-Secret header
# Primary token must be present; CF Access pair is optional (endpoints
# without Cloudflare Access in front don't need them).
_op_load_connect_token() {
    local triple env_var service file val
    local -a triples=(
        "OP_CONNECT_TOKEN|${CCSWITCH_OP_CONNECT_TOKEN_KEYCHAIN_SERVICE}|connect-token"
        "CF_ACCESS_CLIENT_ID|${CCSWITCH_KC_CF_ID_SVC}|cf-access-client-id"
        "CF_ACCESS_CLIENT_SECRET|${CCSWITCH_KC_CF_SECRET_SVC}|cf-access-client-secret"
    )
    for triple in "${triples[@]}"; do
        IFS='|' read -r env_var service file <<<"$triple"
        [[ -n "${!env_var:-}" ]] && continue
        val=$(_load_secret "$service" "$CCSWITCH_OP_CONNECT_TOKEN_KEYCHAIN_ACCOUNT" "$file" 2>/dev/null || true)
        [[ -n "$val" ]] && export "$env_var"="$val"
    done
    [[ -n "${OP_CONNECT_TOKEN:-}" ]]
}

# True if running in Connect mode (OP_CONNECT_HOST configured + token loaded).
# In Connect mode we talk HTTP directly (curl) instead of shelling to `op`.
_op_is_connect() {
    [[ -n "${OP_CONNECT_HOST:-}" ]] && [[ -n "${OP_CONNECT_TOKEN:-}" ]]
}

# ─── HTTP Connect API helpers ──────────────────────────────────────────────
# Some Connect endpoints sit behind Cloudflare Access, which strips
# Authorization: Bearer. We send the bearer in X-OP-Token (a downstream
# proxy remaps it) plus the CF Access service-token pair when configured.
_op_connect_header_args() {
    printf '%s\n' "-H" "X-OP-Token: ${OP_CONNECT_TOKEN}"
    if [[ -n "${CF_ACCESS_CLIENT_ID:-}" ]]; then
        printf '%s\n' "-H" "CF-Access-Client-Id: ${CF_ACCESS_CLIENT_ID}"
    fi
    if [[ -n "${CF_ACCESS_CLIENT_SECRET:-}" ]]; then
        printf '%s\n' "-H" "CF-Access-Client-Secret: ${CF_ACCESS_CLIENT_SECRET}"
    fi
}

# Make a Connect API request. Prints response body on stdout, or nothing
# on HTTP/transport error.
#   _op_connect_api METHOD /v1/path [json-body]
_op_connect_api() {
    local method="$1" path="$2" body="${3:-}"
    local url="${OP_CONNECT_HOST%/}${path}"
    local -a hdrs=()
    mapfile -t hdrs < <(_op_connect_header_args)

    if [[ -n "$body" ]]; then
        curl --fail --silent --show-error \
            "${hdrs[@]}" \
            -H "Content-Type: application/json" \
            -X "$method" \
            --data "$body" \
            "$url" 2>/dev/null
    else
        curl --fail --silent --show-error \
            "${hdrs[@]}" \
            -X "$method" \
            "$url" 2>/dev/null
    fi
}

# Resolve the configured vault name → vault UUID, cached for script lifetime.
_OP_CONNECT_CACHED_VAULT_ID=""
_op_connect_vault_id() {
    if [[ -z "$_OP_CONNECT_CACHED_VAULT_ID" ]]; then
        _OP_CONNECT_CACHED_VAULT_ID=$(
            _op_connect_api GET /v1/vaults \
                | jq -r --arg n "$CCSWITCH_OP_VAULT" '[.[] | select(.name == $n)][0].id // empty' 2>/dev/null
        )
    fi
    [[ -n "$_OP_CONNECT_CACHED_VAULT_ID" ]] && printf '%s' "$_OP_CONNECT_CACHED_VAULT_ID"
}

# Cache title → item UUID resolutions for the script lifetime so a write that
# follows a read of the same item doesn't re-issue the GET-by-title lookup.
declare -gA _OP_CONNECT_ITEM_ID_CACHE=()

# Find an item UUID by title within the configured vault (empty if missing).
# Title is URI-encoded via jq's @uri (jq is already a hard dependency).
# Result cached so a write-after-read on the same title skips the GET.
_op_connect_find_item_id() {
    local title="$1"
    if [[ -n "${_OP_CONNECT_ITEM_ID_CACHE[$title]+set}" ]]; then
        printf '%s' "${_OP_CONNECT_ITEM_ID_CACHE[$title]}"
        return 0
    fi
    local vault_id
    vault_id=$(_op_connect_vault_id) || return 0
    [[ -z "$vault_id" ]] && return 0
    local encoded id
    # `printf %s` avoids the trailing newline that <<< would add, which @uri
    # would otherwise encode as %0A and bake into the filter expression.
    encoded=$(printf '%s' "$title" | jq -sRr @uri)
    id=$(_op_connect_api GET "/v1/vaults/${vault_id}/items?filter=title%20eq%20%22${encoded}%22" \
        | jq -r '.[0].id // empty' 2>/dev/null)
    _OP_CONNECT_ITEM_ID_CACHE[$title]="$id"
    printf '%s' "$id"
}

# Read the `credentials` field of an item by title. Empty on miss.
_op_connect_read() {
    local title="$1"
    local vault_id item_id
    vault_id=$(_op_connect_vault_id) || { echo ""; return; }
    [[ -z "$vault_id" ]] && { echo ""; return; }
    item_id=$(_op_connect_find_item_id "$title")
    [[ -z "$item_id" ]] && { echo ""; return; }
    _op_connect_api GET "/v1/vaults/${vault_id}/items/${item_id}" \
        | jq -r --arg label "$CCSWITCH_OP_CRED_FIELD" \
            '(.fields[]? | select(.label == $label) | .value) // empty' 2>/dev/null
}

# Create-or-update a Secure Note item containing a `credentials` field.
_op_connect_write() {
    local title="$1" credentials="$2"
    local vault_id item_id
    vault_id=$(_op_connect_vault_id) || return 1
    [[ -z "$vault_id" ]] && return 1

    local body
    body=$(jq -n \
        --arg title "$title" \
        --arg vault "$vault_id" \
        --arg value "$credentials" \
        --arg label "$CCSWITCH_OP_CRED_FIELD" \
        '{
            vault: { id: $vault },
            category: "SECURE_NOTE",
            title: $title,
            fields: [
                { label: $label, type: "CONCEALED", value: $value }
            ]
        }')

    item_id=$(_op_connect_find_item_id "$title")
    if [[ -n "$item_id" ]]; then
        _op_connect_api PUT "/v1/vaults/${vault_id}/items/${item_id}" "$body" >/dev/null || return 1
    else
        local created_id
        created_id=$(_op_connect_api POST "/v1/vaults/${vault_id}/items" "$body" \
            | jq -r '.id // empty' 2>/dev/null) || return 1
        # Keep the title→id cache fresh so a subsequent write doesn't re-find.
        [[ -n "$created_id" ]] && _OP_CONNECT_ITEM_ID_CACHE[$title]="$created_id"
    fi
}

# Delete an item by title (no-op if not found).
_op_connect_delete() {
    local title="$1"
    local vault_id item_id
    vault_id=$(_op_connect_vault_id) || return 0
    [[ -z "$vault_id" ]] && return 0
    item_id=$(_op_connect_find_item_id "$title")
    [[ -z "$item_id" ]] && return 0
    _op_connect_api DELETE "/v1/vaults/${vault_id}/items/${item_id}" >/dev/null || true
    unset "_OP_CONNECT_ITEM_ID_CACHE[$title]"
}

# Load config immediately so all subsequent code sees resolved values
_load_config

# Container detection
is_running_in_container() {
    # Check for Docker environment file
    if [[ -f /.dockerenv ]]; then
        return 0
    fi
    
    # Check cgroup for container indicators
    if [[ -f /proc/1/cgroup ]] && grep -q 'docker\|lxc\|containerd\|kubepods' /proc/1/cgroup 2>/dev/null; then
        return 0
    fi
    
    # Check mount info for container filesystems
    if [[ -f /proc/self/mountinfo ]] && grep -q 'docker\|overlay' /proc/self/mountinfo 2>/dev/null; then
        return 0
    fi
    
    # Check for common container environment variables
    if [[ -n "${CONTAINER:-}" ]] || [[ -n "${container:-}" ]]; then
        return 0
    fi
    
    return 1
}

# Platform detection
detect_platform() {
    case "$(uname -s)" in
        Darwin) echo "macos" ;;
        Linux) 
            if [[ -n "${WSL_DISTRO_NAME:-}" ]]; then
                echo "wsl"
            else
                echo "linux"
            fi
            ;;
        *) echo "unknown" ;;
    esac
}

# Get Claude configuration file path with fallback
get_claude_config_path() {
    local primary_config="$HOME/.claude/.claude.json"
    local fallback_config="$HOME/.claude.json"
    
    # Check primary location first
    if [[ -f "$primary_config" ]]; then
        # Verify it has valid oauthAccount structure
        if jq -e '.oauthAccount' "$primary_config" >/dev/null 2>&1; then
            echo "$primary_config"
            return
        fi
    fi
    
    # Fallback to standard location
    echo "$fallback_config"
}

# Basic validation that JSON is valid
validate_json() {
    local file="$1"
    if ! jq . "$file" >/dev/null 2>&1; then
        echo "Error: Invalid JSON in $file"
        return 1
    fi
}

# Email validation function
validate_email() {
    local email="$1"
    # Use robust regex for email validation
    if [[ "$email" =~ ^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$ ]]; then
        return 0
    else
        return 1
    fi
}

# Resolve account identifier (hash, email, or numeric index for backward compat) to hash
resolve_account_identifier() {
    local identifier="$1"
    [[ ! -f "$SEQUENCE_FILE" ]] && { echo ""; return; }

    # Case 1: Already a hash (8 hex chars) - verify it exists
    if [[ "$identifier" =~ ^[0-9a-f]{8}$ ]]; then
        if jq -e --arg id "$identifier" '.accounts[$id]' "$SEQUENCE_FILE" >/dev/null 2>&1; then
            echo "$identifier"
            return
        fi
    fi

    # Case 2: Email address - hash it and verify
    if [[ "$identifier" == *@* ]]; then
        local hash
        hash=$(hash_email "$identifier")
        if jq -e --arg id "$hash" '.accounts[$id]' "$SEQUENCE_FILE" >/dev/null 2>&1; then
            echo "$hash"
            return
        fi
        # Not found by hash - scan accounts for matching email (legacy/migration)
        local found
        found=$(jq -r --arg email "$identifier" '.accounts | to_entries[] | select(.value.email == $email) | .key' "$SEQUENCE_FILE" 2>/dev/null)
        if [[ -n "$found" && "$found" != "null" ]]; then
            echo "$found"
            return
        fi
    fi

    # Case 3: Numeric index into sequence (backward compat) - 1-based
    if [[ "$identifier" =~ ^[0-9]+$ ]]; then
        local idx=$((identifier - 1))
        local found
        found=$(jq -r --arg i "$idx" '.sequence[$i | tonumber] // empty' "$SEQUENCE_FILE" 2>/dev/null)
        if [[ -n "$found" && "$found" != "null" ]]; then
            echo "$found"
            return
        fi
    fi

    echo ""
}

# Get the email for a given account ID
get_account_email() {
    local id="$1"
    jq -r --arg id "$id" '.accounts[$id].email // empty' "$SEQUENCE_FILE" 2>/dev/null
}

# Safe JSON write with validation
write_json() {
    local file="$1"
    local content="$2"
    local temp_file
    temp_file=$(mktemp "${file}.XXXXXX")
    
    echo "$content" > "$temp_file"
    if ! jq . "$temp_file" >/dev/null 2>&1; then
        rm -f "$temp_file"
        echo "Error: Generated invalid JSON"
        return 1
    fi
    
    mv "$temp_file" "$file"
    chmod 600 "$file"
}

# Check Bash version (4.4+ required)
check_bash_version() {
    local version
    version=$(bash --version | head -n1 | grep -oE '[0-9]+\.[0-9]+' | head -n1)
    if ! awk -v ver="$version" 'BEGIN { exit (ver >= 4.4 ? 0 : 1) }'; then
        echo "Error: Bash 4.4+ required (found $version)"
        exit 1
    fi
}

# Check dependencies
check_dependencies() {
    for cmd in jq; do
        if ! command -v "$cmd" >/dev/null 2>&1; then
            echo "Error: Required command '$cmd' not found"
            echo "Install with: apt install $cmd (Linux) or brew install $cmd (macOS)"
            exit 1
        fi
    done
}

# Setup backup directories
setup_directories() {
    mkdir -p "$BACKUP_DIR"/{configs,credentials}
    chmod 700 "$BACKUP_DIR"
    chmod 700 "$BACKUP_DIR"/{configs,credentials}
}

# Claude Code process detection (Node.js app)
is_claude_running() {
    ps -eo pid,comm,args | awk '$2 == "claude" || $3 == "claude" {exit 0} END {exit 1}'
}

# Wait for Claude Code to close (no timeout - user controlled)
wait_for_claude_close() {
    if ! is_claude_running; then
        return 0
    fi
    
    echo "Claude Code is running. Please close it first."
    echo "Waiting for Claude Code to close..."
    
    while is_claude_running; do
        sleep 1
    done
    
    echo "Claude Code closed. Continuing..."
}

# Get current account info from .claude.json
get_current_account() {
    if [[ ! -f "$(get_claude_config_path)" ]]; then
        echo "none"
        return
    fi
    
    if ! validate_json "$(get_claude_config_path)"; then
        echo "none"
        return
    fi
    
    local email
    email=$(jq -r '.oauthAccount.emailAddress // empty' "$(get_claude_config_path)" 2>/dev/null)
    echo "${email:-none}"
}

# Resolve effective backend (auto → keychain/file based on platform)
_resolve_backend() {
    if [[ "$CCSWITCH_BACKEND" == "auto" ]]; then
        case "$(detect_platform)" in
            macos) echo "keychain" ;;
            *) echo "file" ;;
        esac
    else
        echo "$CCSWITCH_BACKEND"
    fi
}

# ═══════════════════════════════════════════════════════════════════════════
# Credential Backend: Keychain (macOS)
# ═══════════════════════════════════════════════════════════════════════════
_keychain_read() {
    local service="$1"
    security find-generic-password -s "$service" -w 2>/dev/null || echo ""
}

_keychain_write() {
    local service="$1" credentials="$2"
    # $USER may be unset in stripped environments (launchd agents, CI, tests);
    # fall back to `id -un` which always works.
    local acct="${USER:-$(id -un 2>/dev/null)}"
    security add-generic-password -U -s "$service" -a "$acct" -w "$credentials" 2>/dev/null
}

_keychain_delete() {
    local service="$1"
    # `security delete-generic-password` dumps the deleted item's attributes
    # on stdout; silence both streams so callers don't get surprise output.
    security delete-generic-password -s "$service" &>/dev/null || true
}

# ═══════════════════════════════════════════════════════════════════════════
# Credential Backend: File (Linux/WSL)
# ═══════════════════════════════════════════════════════════════════════════
_file_read() {
    local filepath="$1"
    if [[ -f "$filepath" ]]; then
        cat "$filepath"
    else
        echo ""
    fi
}

_file_write() {
    local filepath="$1" credentials="$2"
    mkdir -p "$(dirname "$filepath")"
    printf '%s' "$credentials" > "$filepath"
    chmod 600 "$filepath"
}

_file_delete() {
    local filepath="$1"
    rm -f "$filepath"
}

# ═══════════════════════════════════════════════════════════════════════════
# Credential Backend: 1Password
# ═══════════════════════════════════════════════════════════════════════════
_op_item_name() {
    local account_label="$1"
    echo "${CCSWITCH_OP_ITEM_PREFIX} - ${account_label}"
}

# Build op CLI args for signed-in mode. In Connect mode --account is
# forbidden (token is scoped to a server), so this returns nothing.
_op_args() {
    if ! _op_is_connect && [[ -n "${CCSWITCH_OP_ACCOUNT:-}" ]]; then
        printf '%s\n%s\n' "--account" "$CCSWITCH_OP_ACCOUNT"
    fi
}

_op_read() {
    local item_name="$1"
    if _op_is_connect; then
        _op_connect_read "$item_name"
        return
    fi
    if ! command -v op &>/dev/null; then
        echo ""
        return
    fi
    local -a op_args; mapfile -t op_args < <(_op_args)
    op item get "$item_name" "${op_args[@]}" --vault "$CCSWITCH_OP_VAULT" --fields "label=$CCSWITCH_OP_CRED_FIELD" --format json 2>/dev/null | jq -r '.value // empty' 2>/dev/null || echo ""
}

_op_write() {
    local item_name="$1" credentials="$2"
    if _op_is_connect; then
        _op_connect_write "$item_name" "$credentials"
        return
    fi
    if ! command -v op &>/dev/null; then
        echo "Error: 1password-cli (op) not installed" >&2
        return 1
    fi
    local -a op_args; mapfile -t op_args < <(_op_args)
    if op item get "$item_name" "${op_args[@]}" --vault "$CCSWITCH_OP_VAULT" &>/dev/null 2>&1; then
        op item edit "$item_name" "${op_args[@]}" --vault "$CCSWITCH_OP_VAULT" "${CCSWITCH_OP_CRED_FIELD}=$credentials" &>/dev/null
    else
        op item create "${op_args[@]}" --category "Secure Note" --title "$item_name" --vault "$CCSWITCH_OP_VAULT" "${CCSWITCH_OP_CRED_FIELD}=$credentials" &>/dev/null
    fi
}

_op_delete() {
    local item_name="$1"
    if _op_is_connect; then
        _op_connect_delete "$item_name"
        return
    fi
    if ! command -v op &>/dev/null; then
        return 0
    fi
    local -a op_args; mapfile -t op_args < <(_op_args)
    op item delete "$item_name" "${op_args[@]}" --vault "$CCSWITCH_OP_VAULT" &>/dev/null 2>&1 || true
}

# ═══════════════════════════════════════════════════════════════════════════
# Credential Backend: HashiCorp Vault / OpenBao
# ═══════════════════════════════════════════════════════════════════════════
_vault_cmd() {
    # Use bao if available (OpenBao), fall back to vault
    if command -v bao &>/dev/null; then
        echo "bao"
    elif command -v vault &>/dev/null; then
        echo "vault"
    else
        echo ""
    fi
}

_vault_read() {
    local key="$1"
    local cmd
    cmd=$(_vault_cmd)
    if [[ -z "$cmd" ]]; then
        echo ""
        return
    fi
    VAULT_ADDR="$CCSWITCH_VAULT_ADDR" VAULT_TOKEN="$CCSWITCH_VAULT_TOKEN" \
        "$cmd" kv get -field=credentials "${CCSWITCH_VAULT_PATH}/${key}" 2>/dev/null || echo ""
}

_vault_write() {
    local key="$1" credentials="$2"
    local cmd
    cmd=$(_vault_cmd)
    if [[ -z "$cmd" ]]; then
        echo "Error: neither vault nor bao CLI installed" >&2
        return 1
    fi
    VAULT_ADDR="$CCSWITCH_VAULT_ADDR" VAULT_TOKEN="$CCSWITCH_VAULT_TOKEN" \
        "$cmd" kv put "${CCSWITCH_VAULT_PATH}/${key}" credentials="$credentials" &>/dev/null
}

_vault_delete() {
    local key="$1"
    local cmd
    cmd=$(_vault_cmd)
    if [[ -n "$cmd" ]]; then
        VAULT_ADDR="$CCSWITCH_VAULT_ADDR" VAULT_TOKEN="$CCSWITCH_VAULT_TOKEN" \
            "$cmd" kv delete "${CCSWITCH_VAULT_PATH}/${key}" &>/dev/null 2>&1 || true
    fi
}

# ═══════════════════════════════════════════════════════════════════════════
# Unified Credential API
# ═══════════════════════════════════════════════════════════════════════════

# Read active Claude Code credentials
read_credentials() {
    local backend
    backend=$(_resolve_backend)
    case "$backend" in
        keychain)  _keychain_read "Claude Code-credentials" ;;
        file)      _file_read "$HOME/.claude/.credentials.json" ;;
        1password) _op_read "$(_op_item_name "active")" ;;
        vault)     _vault_read "active" ;;
    esac
}

# Write active Claude Code credentials
write_credentials() {
    local credentials="$1"
    local backend
    backend=$(_resolve_backend)
    case "$backend" in
        keychain)  _keychain_write "Claude Code-credentials" "$credentials" ;;
        file)      _file_write "$HOME/.claude/.credentials.json" "$credentials" ;;
        1password) _op_write "$(_op_item_name "active")" "$credentials" ;;
        vault)     _vault_write "active" "$credentials" ;;
    esac
}

# Read per-account backup credentials
read_account_credentials() {
    local account_num="$1" email="$2"
    local backend
    backend=$(_resolve_backend)
    case "$backend" in
        keychain)  _keychain_read "Claude Code-Account-${account_num}-${email}" ;;
        file)      _file_read "$BACKUP_DIR/credentials/.claude-credentials-${account_num}-${email}.json" ;;
        1password) _op_read "$(_op_item_name "${account_num}-${email}")" ;;
        vault)     _vault_read "account-${account_num}-${email}" ;;
    esac
}

# Write per-account backup credentials
write_account_credentials() {
    local account_num="$1" email="$2" credentials="$3"
    local backend
    backend=$(_resolve_backend)
    case "$backend" in
        keychain)  _keychain_write "Claude Code-Account-${account_num}-${email}" "$credentials" ;;
        file)      _file_write "$BACKUP_DIR/credentials/.claude-credentials-${account_num}-${email}.json" "$credentials" ;;
        1password) _op_write "$(_op_item_name "${account_num}-${email}")" "$credentials" ;;
        vault)     _vault_write "account-${account_num}-${email}" "$credentials" ;;
    esac
}

# Delete per-account backup credentials
delete_account_credentials() {
    local account_num="$1" email="$2"
    local backend
    backend=$(_resolve_backend)
    case "$backend" in
        keychain)  _keychain_delete "Claude Code-Account-${account_num}-${email}" ;;
        file)      _file_delete "$BACKUP_DIR/credentials/.claude-credentials-${account_num}-${email}.json" ;;
        1password) _op_delete "$(_op_item_name "${account_num}-${email}")" ;;
        vault)     _vault_delete "account-${account_num}-${email}" ;;
    esac
}

# ═══════════════════════════════════════════════════════════════════════════
# OAuth Token Refresh (via Claude CLI — the supported path)
# ═══════════════════════════════════════════════════════════════════════════
#
# We do NOT call the OAuth refresh endpoint directly. Per Anthropic:
# - Direct refresh calls can invalidate Claude Code's active token
# - Refresh tokens are single-use; concurrent refreshes race
# - The supported automation path is `claude auth login` with env vars
#   or long-lived setup tokens via `claude setup-token`
#
# Supported refresh methods:
#   1. `claude auth login` with CLAUDE_CODE_OAUTH_REFRESH_TOKEN env var
#   2. Long-lived CLAUDE_CODE_OAUTH_TOKEN from `claude setup-token`
#   3. Interactive `claude auth login` (browser flow, used by --login)

readonly OAUTH_SCOPES="user:profile user:inference user:sessions:claude_code"

# Check if a credential JSON blob has an expired access token (within 5 min).
_token_is_expired() {
    local cred_json="$1"
    local exp_ms now_ms
    exp_ms=$(_cred_expires_ms "$cred_json")
    now_ms=$(awk 'BEGIN { printf "%d", systime() * 1000 }')
    [[ "$exp_ms" -lt $((now_ms + 300000)) ]]
}

# Refresh credentials using `claude auth login` with the refresh token env var.
# Uses an isolated CLAUDE_CONFIG_DIR so the active session is never touched.
# The refreshed credentials are written to .credentials.json inside the temp dir.
# Returns the refreshed credential JSON on stdout, or empty on failure.
_refresh_via_claude_auth() {
    local cred_json="$1"

    local refresh_token
    refresh_token=$(_cred_field "$cred_json" refreshToken)

    if [[ -z "$refresh_token" ]]; then
        echo "no refresh token" >&2
        return 1
    fi

    # Create an isolated config dir so we don't touch the active keychain/session
    local tmp_config tmp_work
    tmp_config=$(mktemp -d -t ccswitch-refresh-config.XXXXXX)
    tmp_work=$(mktemp -d -t ccswitch-refresh-work.XXXXXX)

    # Seed with the existing credential blob so claude can read it
    echo "$cred_json" > "$tmp_config/.credentials.json"
    chmod 600 "$tmp_config/.credentials.json"
    echo '{"hasCompletedOnboarding":true}' > "$tmp_config/.claude.json"

    # Run `claude auth login` in the isolated config dir.
    # Claude reads the refresh token from the env var, exchanges it,
    # and writes the new credentials to $tmp_config/.credentials.json
    (
        cd "$tmp_work"
        CLAUDE_CONFIG_DIR="$tmp_config" \
        CLAUDE_CODE_OAUTH_REFRESH_TOKEN="$refresh_token" \
        CLAUDE_CODE_OAUTH_SCOPES="$OAUTH_SCOPES" \
        claude auth login
    ) >/dev/null 2>&1
    local exit_code=$?

    if [[ $exit_code -eq 0 ]] && [[ -f "$tmp_config/.credentials.json" ]]; then
        cat "$tmp_config/.credentials.json"
    fi

    rm -rf "$tmp_config" "$tmp_work"
    return $exit_code
}

# Refresh credentials for a specific account. Updates the backend.
# Uses `claude auth login` in an isolated CLAUDE_CONFIG_DIR — never touches
# the active keychain or disrupts running sessions.
refresh_account_token() {
    local account_id="$1"
    local email
    email=$(get_account_email "$account_id")
    local active_id
    active_id=$(jq -r '.activeAccountId' "$SEQUENCE_FILE")

    # Read current credentials from the appropriate source
    local cred_json
    if [[ "$account_id" == "$active_id" ]]; then
        cred_json=$(read_credentials)
    else
        cred_json=$(read_account_credentials "$account_id" "$email")
    fi

    if [[ -z "$cred_json" ]]; then
        return 1
    fi

    if ! _token_is_expired "$cred_json"; then
        return 0  # Not expired, nothing to do
    fi

    if ! command -v claude &>/dev/null; then
        echo "claude CLI not found" >&2
        return 1
    fi

    # Refresh in isolated config dir (no keychain swap needed)
    local new_creds
    new_creds=$(_refresh_via_claude_auth "$cred_json")

    if [[ -n "$new_creds" ]] && ! _token_is_expired "$new_creds"; then
        # Write refreshed credentials to the backend
        if [[ "$account_id" == "$active_id" ]]; then
            write_credentials "$new_creds"
        fi
        write_account_credentials "$account_id" "$email" "$new_creds"
        return 0
    fi

    return 1
}

# Save current active credentials to the backup slot
# Run this after logging in or re-authenticating to capture fresh tokens
cmd_save() {
    local active_id
    active_id=$(jq -r '.activeAccountId' "$SEQUENCE_FILE")
    local expected_email
    expected_email=$(get_account_email "$active_id")

    local current_email
    current_email=$(get_current_account)

    if [[ "$current_email" == "none" ]]; then
        echo "Error: No active Claude account found"
        return 1
    fi

    local target_id="$active_id"
    local target_email="$expected_email"

    # Verify identity matches the expected active account
    if [[ "$current_email" != "$expected_email" ]]; then
        echo "Warning: Active account ($current_email) doesn't match expected ($expected_email)"
        echo "The active keychain may have been updated by a different account login."

        # Find account by current email (compute hash)
        local current_hash
        current_hash=$(hash_email "$current_email")
        if jq -e --arg id "$current_hash" '.accounts[$id]' "$SEQUENCE_FILE" >/dev/null 2>&1; then
            target_id="$current_hash"
            target_email="$current_email"
            # Update activeAccountId to match reality
            local updated
            updated=$(jq --arg id "$target_id" '.activeAccountId = $id' "$SEQUENCE_FILE")
            write_json "$SEQUENCE_FILE" "$updated"
            echo "Updated active account to ${target_id} (${target_email})"
        else
            echo "Error: Current account ${current_email} is not managed. Run --add-account first."
            return 1
        fi
    fi

    local creds config_content
    creds=$(read_credentials)
    config_content=$(cat "$(get_claude_config_path)")

    if [[ -z "$creds" ]]; then
        echo "Error: No credentials in active slot"
        return 1
    fi

    write_account_credentials "$target_id" "$target_email" "$creds"
    write_account_config "$target_id" "$target_email" "$config_content"

    local expires_h
    expires_h=$(_cred_hours_left "$(_cred_expires_ms "$creds")")

    echo "Saved credentials for ${target_id} (${target_email}, expires in ${expires_h}h)"
}

# Interactive re-login for accounts with expired/invalid credentials.
# For each expired account, launches `claude` in interactive mode so the user
# can log in via the browser. After claude exits, captures the fresh
# credentials and writes them to the backup slot. Rotates through all
# expired accounts (or a specific one if --only <id> is passed).
cmd_login() {
    if ! command -v claude &>/dev/null; then
        echo "Error: claude CLI not found"
        return 1
    fi

    local only_id=""
    local force="false"
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --only)
                shift
                only_id=$(resolve_account_identifier "${1:-}")
                [[ -z "$only_id" ]] && { echo "Error: account not found: $1"; return 1; }
                ;;
            --force)
                force="true"  # Re-login even if not expired
                ;;
        esac
        shift
    done

    local account_ids
    account_ids=$(jq -r '.sequence[]' "$SEQUENCE_FILE")

    # Build list of accounts needing login
    local -a todo=()
    for id in $account_ids; do
        [[ -n "$only_id" ]] && [[ "$id" != "$only_id" ]] && continue

        local email
        email=$(get_account_email "$id")
        local cred_json
        cred_json=$(read_account_credentials "$id" "$email")

        if [[ "$force" == "true" ]] || [[ -z "$cred_json" ]] || _token_is_expired "$cred_json"; then
            todo+=("${id}|${email}")
        fi
    done

    if [[ ${#todo[@]} -eq 0 ]]; then
        echo "All accounts have valid credentials. Use --force to re-login anyway."
        return 0
    fi

    echo "Found ${#todo[@]} account(s) needing login:"
    for entry in "${todo[@]}"; do
        echo "  ${entry%|*} ${entry#*|}"
    done
    echo ""

    local i=0
    for entry in "${todo[@]}"; do
        i=$((i + 1))
        local id="${entry%|*}"
        local email="${entry#*|}"

        echo ""
        echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
        echo "[${i}/${#todo[@]}] Logging in: ${id} (${email})"
        echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
        echo ""

        # Use the real switch mechanism: swap this account into the active slot,
        # then launch claude which will handle login/refresh via its native auth.
        # After claude exits, save the (now fresh) credentials back to the backup.

        local original_active
        original_active=$(jq -r '.activeAccountId' "$SEQUENCE_FILE")

        # Switch to this account (puts its backup creds into the active keychain slot)
        echo "Switching to ${id} (${email})..."
        perform_switch "$id" >/dev/null 2>&1 || true

        echo ""
        echo "Launching claude for interactive login as ${email}..."
        echo "  - You will be prompted to log in via your browser"
        echo "  - Make sure to log in as: ${email}"
        echo "  - Type /exit or press Ctrl+D when done"
        echo ""

        # Launch claude in a temp directory so we don't pollute real project sessions
        local login_workdir
        login_workdir=$(mktemp -d -t ccswitch-login-session.XXXXXX)
        (cd "$login_workdir" && claude) || true
        rm -rf "$login_workdir"

        # After claude exits, capture the fresh credentials from the active keychain slot
        local new_creds
        new_creds=$(read_credentials)
        if [[ -n "$new_creds" ]] && ! _token_is_expired "$new_creds"; then
            # Verify the logged-in email matches
            local actual_email
            actual_email=$(get_current_account)
            if [[ "$actual_email" != "$email" ]] && [[ "$actual_email" != "none" ]]; then
                echo ""
                echo "⚠ Warning: Logged in as ${actual_email}, expected ${email}"
            fi

            # Save fresh credentials to the backup slot
            write_account_credentials "$id" "$email" "$new_creds"
            local config_content
            config_content=$(cat "$(get_claude_config_path)")
            write_account_config "$id" "$email" "$config_content"
            echo ""
            echo "✓ Credentials saved for ${id} (${email})"
        else
            echo ""
            echo "✗ No fresh credentials captured for ${id}"
        fi
    done

    # Restore the originally active account
    if [[ -n "$original_active" ]] && [[ "$original_active" != "null" ]]; then
        local current_active
        current_active=$(jq -r '.activeAccountId' "$SEQUENCE_FILE")
        if [[ "$current_active" != "$original_active" ]]; then
            echo ""
            echo "Restoring original account $(get_account_email "$original_active")..."
            perform_switch "$original_active" >/dev/null 2>&1 || true
        fi
    fi

    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "Interactive login complete. Run 'ccswitch --usage-all' to verify."
}

# Refresh all accounts with expired tokens using claude CLI
cmd_refresh_all() {
    if ! command -v claude &>/dev/null; then
        echo "Error: claude CLI not found"
        return 1
    fi

    local account_ids
    account_ids=$(jq -r '.sequence[]' "$SEQUENCE_FILE")
    local active_id
    active_id=$(jq -r '.activeAccountId' "$SEQUENCE_FILE")

    echo "Refreshing tokens via claude CLI..."
    echo ""

    # Step 1: Sync active account's fresh credentials to its backup slot.
    # This is the most important step - Claude Code keeps the active slot fresh,
    # but the backup can go stale. This copy keeps them in sync.
    if [[ -n "$active_id" ]] && [[ "$active_id" != "null" ]]; then
        local active_email
        active_email=$(get_account_email "$active_id")
        echo -n "  #${active_id} ${active_email} (active): "
        local active_creds
        active_creds=$(read_credentials)
        if [[ -n "$active_creds" ]]; then
            write_account_credentials "$active_id" "$active_email" "$active_creds"
            local hours_left
            hours_left=$(_cred_hours_left "$(_cred_expires_ms "$active_creds")")
            echo "synced to backup (${hours_left}h remaining)"
        else
            echo "no active credentials"
        fi
    fi

    # Step 2: For each non-active account, check token state and try to refresh
    for id in $account_ids; do
        [[ "$id" == "$active_id" ]] && continue

        local email
        email=$(get_account_email "$id")
        echo -n "  #${id} ${email}: "

        local cred_json
        cred_json=$(read_account_credentials "$id" "$email")

        if [[ -z "$cred_json" ]]; then
            echo "no credentials"
            continue
        fi

        if ! _token_is_expired "$cred_json"; then
            local hours_left
            hours_left=$(_cred_hours_left "$(_cred_expires_ms "$cred_json")")
            echo "valid (${hours_left}h remaining)"
            continue
        fi

        # Token expired - try refreshing via claude CLI
        echo -n "expired, refreshing via claude auth login... "
        if refresh_account_token "$id"; then
            echo "✓"
        else
            echo "✗ (run 'ccswitch --login --only ${id}' to re-auth via browser)"
        fi

        sleep 1
    done
}

# Read account config from backup
read_account_config() {
    local account_num="$1"
    local email="$2"
    local config_file="$BACKUP_DIR/configs/.claude-config-${account_num}-${email}.json"
    
    if [[ -f "$config_file" ]]; then
        cat "$config_file"
    else
        echo ""
    fi
}

# Write account config to backup
write_account_config() {
    local account_num="$1"
    local email="$2"
    local config="$3"
    local config_file="$BACKUP_DIR/configs/.claude-config-${account_num}-${email}.json"
    
    echo "$config" > "$config_file"
    chmod 600 "$config_file"
}

# Initialize sequence.json if it doesn't exist
init_sequence_file() {
    if [[ ! -f "$SEQUENCE_FILE" ]]; then
        local init_content='{
  "version": '$SCHEMA_VERSION',
  "activeAccountId": null,
  "lastUpdated": "'$(date -u +%Y-%m-%dT%H:%M:%SZ)'",
  "sequence": [],
  "accounts": {}
}'
        write_json "$SEQUENCE_FILE" "$init_content"
    fi

    # Auto-migrate v1 schema (numeric keys) to v2 schema (hash keys)
    migrate_v1_to_v2
}

# Migrate from v1 (numeric account keys) to v2 (email-hash keys)
migrate_v1_to_v2() {
    [[ ! -f "$SEQUENCE_FILE" ]] && return

    local version
    version=$(jq -r '.version // 1' "$SEQUENCE_FILE" 2>/dev/null)
    [[ "$version" == "$SCHEMA_VERSION" ]] && return

    # Detect v1: has numeric account keys like "1", "2", "3"
    local has_numeric
    has_numeric=$(jq -r '.accounts | keys | map(select(test("^[0-9]+$"))) | length' "$SEQUENCE_FILE" 2>/dev/null)
    [[ "$has_numeric" == "0" ]] && return

    echo "Migrating sequence.json from v1 to v2 (email-hash keys)..."

    # Build the new structure with hash keys
    # Note: we read both old (activeAccountNumber) and new (activeAccountId) field names
    # because sed-based refactoring may have renamed the field but kept old data
    local accounts_v1 active_v1 sequence_v1
    accounts_v1=$(jq -c '.accounts' "$SEQUENCE_FILE")
    active_v1=$(jq -r '.activeAccountNumber // .activeAccountId // empty' "$SEQUENCE_FILE")
    sequence_v1=$(jq -c '.sequence // []' "$SEQUENCE_FILE")

    # Python handles the restructuring + keychain/file renames
    local platform
    platform=$(detect_platform)
    python3 -c "
import json, sys, hashlib, subprocess, os

BACKUP_DIR = '$BACKUP_DIR'
SEQUENCE_FILE = '$SEQUENCE_FILE'
PLATFORM = '$platform'

with open(SEQUENCE_FILE) as f:
    data = json.load(f)

def h(email):
    return hashlib.sha256(email.encode()).hexdigest()[:8]

old_accounts = data.get('accounts', {})
new_accounts = {}
num_to_hash = {}

# Convert accounts dict from numeric to hash keys
for num_key, acct in old_accounts.items():
    email = acct.get('email', '')
    if not email:
        continue
    hash_id = h(email)
    num_to_hash[num_key] = hash_id
    new_accounts[hash_id] = acct

# Convert sequence array
old_sequence = data.get('sequence', [])
new_sequence = []
for item in old_sequence:
    key = str(item)
    if key in num_to_hash:
        new_sequence.append(num_to_hash[key])

# Convert activeAccountNumber/Id to activeAccountId
active_num = data.get('activeAccountNumber') or data.get('activeAccountId')
active_id = None
if active_num is not None:
    # If it's already a hash (migrated), keep it. If numeric, look up hash.
    if str(active_num) in num_to_hash:
        active_id = num_to_hash[str(active_num)]
    elif str(active_num) in new_accounts:
        active_id = str(active_num)

# Convert switchLog (from/to were numbers)
old_log = data.get('switchLog', [])
new_log = []
for entry in old_log:
    from_num = str(entry.get('from', ''))
    to_num = str(entry.get('to', ''))
    new_log.append({
        'from': num_to_hash.get(from_num, from_num),
        'to': num_to_hash.get(to_num, to_num),
        'at': entry.get('at', ''),
    })

# Build new v2 structure
new_data = {
    'version': 2,
    'activeAccountId': active_id,
    'lastUpdated': data.get('lastUpdated', ''),
    'sequence': new_sequence,
    'accounts': new_accounts,
}
if new_log:
    new_data['switchLog'] = new_log

with open(SEQUENCE_FILE, 'w') as f:
    json.dump(new_data, f, indent=2)

# Rename keychain entries and config backup files
for num_key, hash_id in num_to_hash.items():
    email = new_accounts[hash_id].get('email', '')
    old_name = f'Claude Code-Account-{num_key}-{email}'
    new_name = f'Claude Code-Account-{hash_id}-{email}'

    if PLATFORM == 'macos':
        # Read old credential
        try:
            r = subprocess.run(['security', 'find-generic-password', '-s', old_name, '-w'],
                             capture_output=True, text=True)
            if r.returncode == 0 and r.stdout.strip():
                cred = r.stdout.strip()
                # Write to new name
                subprocess.run(['security', 'add-generic-password', '-U', '-s', new_name,
                              '-a', os.environ.get('USER', ''), '-w', cred],
                             capture_output=True)
                # Delete old
                subprocess.run(['security', 'delete-generic-password', '-s', old_name],
                             capture_output=True)
                print(f'  Renamed keychain: {old_name} -> {new_name}')
        except Exception as e:
            print(f'  Failed to migrate keychain for {email}: {e}')
    else:
        old_file = f'{BACKUP_DIR}/credentials/.claude-credentials-{num_key}-{email}.json'
        new_file = f'{BACKUP_DIR}/credentials/.claude-credentials-{hash_id}-{email}.json'
        if os.path.exists(old_file):
            os.rename(old_file, new_file)
            print(f'  Renamed file: {old_file} -> {new_file}')

    # Rename config backup file
    old_config = f'{BACKUP_DIR}/configs/.claude-config-{num_key}-{email}.json'
    new_config = f'{BACKUP_DIR}/configs/.claude-config-{hash_id}-{email}.json'
    if os.path.exists(old_config):
        os.rename(old_config, new_config)
        print(f'  Renamed config: {old_config} -> {new_config}')

print(f'Migration complete: {len(num_to_hash)} accounts converted')
" 2>&1
}

# Get the hash ID for a new account (deterministic from email)
get_next_account_id() {
    local email="$1"
    hash_email "$email"
}

# Check if account exists by email
account_exists() {
    local email="$1"
    if [[ ! -f "$SEQUENCE_FILE" ]]; then
        return 1
    fi
    
    jq -e --arg email "$email" '.accounts[] | select(.email == $email)' "$SEQUENCE_FILE" >/dev/null 2>&1
}

# ═══════════════════════════════════════════════════════════════════════════
# 1Password Sync
# Push local credentials + sequence.json to 1Password; pull them back.
# "Newest wins" based on credential expiresAt + sequence.json lastUpdated.
# ═══════════════════════════════════════════════════════════════════════════

# Name of the 1Password item that holds sequence.json metadata
_op_sequence_item_name() {
    echo "${CCSWITCH_OP_ITEM_PREFIX} - _sequence"
}

# Check that the configured 1Password backend is reachable.
# Probe uses GET /v1/vaults in Connect mode, `op vault list` otherwise.
_op_check() {
    if [[ -n "${CCSWITCH_OP_CONNECT_HOST:-}" ]] && [[ -z "${OP_CONNECT_TOKEN:-}" ]]; then
        echo "Error: OP_CONNECT_HOST is configured (${CCSWITCH_OP_CONNECT_HOST}) but no OP_CONNECT_TOKEN found." >&2
        echo "Run: ccswitch --setup-op-connect" >&2
        return 1
    fi

    if _op_is_connect; then
        if ! command -v curl &>/dev/null; then
            echo "Error: curl not installed — required for Connect mode" >&2
            return 1
        fi
        if ! _op_connect_api GET /v1/vaults >/dev/null; then
            echo "Error: Connect probe failed at ${OP_CONNECT_HOST}/v1/vaults." >&2
            echo "Check URL, token, and CF Access service-token headers." >&2
            return 1
        fi
        return 0
    fi

    if ! command -v op &>/dev/null; then
        echo "Error: 1password-cli (op) not installed" >&2
        return 1
    fi
    local -a op_args; mapfile -t op_args < <(_op_args)
    if ! op vault list "${op_args[@]}" --format=json &>/dev/null; then
        local target="${CCSWITCH_OP_ACCOUNT:-default account}"
        echo "Error: 1Password CLI not signed in for ${target}." >&2
        echo "Run: op ${op_args[*]} signin" >&2
        return 1
    fi
    return 0
}

# Extract a string field from `.claudeAiOauth` of a credential JSON blob.
# Empty if blob is empty, malformed, or field is missing.
_cred_field() {
    local cred_json="$1" key="$2"
    [[ -z "$cred_json" ]] && return 0
    jq -r --arg k "$key" '(.claudeAiOauth[$k] // empty)' <<<"$cred_json" 2>/dev/null
}

# Extract expiresAt (ms since epoch) from a credential JSON blob, 0 if missing.
_cred_expires_ms() {
    local cred_json="$1"
    [[ -z "$cred_json" ]] && { echo "0"; return; }
    jq -r '(.claudeAiOauth.expiresAt // 0)' <<<"$cred_json" 2>/dev/null || echo 0
}

# Hours from now until the credential expires (one decimal place). Uses awk
# so we don't fork python just for a clock subtract.
_cred_hours_left() {
    local exp_ms="$1"
    awk -v exp="$exp_ms" 'BEGIN { printf "%.1f", (exp - systime()*1000) / 3600000 }'
}

# ═══════════════════════════════════════════════════════════════════════════
# Config Commands
# ═══════════════════════════════════════════════════════════════════════════

# Print the effective configuration (merged from defaults + TOML + env)
cmd_config() {
    local resolved_backend
    resolved_backend=$(_resolve_backend)

    echo "ccswitch configuration:"
    echo ""
    echo "  Config file:    ${CCSWITCH_CONFIG_FILE}"
    if [[ -f "$CCSWITCH_CONFIG_FILE" ]]; then
        echo "  Config exists:  yes"
    else
        echo "  Config exists:  no (using defaults — run 'ccswitch --init-config' to create)"
    fi
    echo ""
    echo "[backend]"
    echo "  type = \"${CCSWITCH_BACKEND}\"   (resolved: ${resolved_backend})"
    echo ""
    if [[ "$resolved_backend" == "1password" ]] || [[ "$CCSWITCH_BACKEND" == "1password" ]]; then
        echo "[backend.onepassword]"
        echo "  vault       = \"${CCSWITCH_OP_VAULT}\""
        echo "  item_prefix = \"${CCSWITCH_OP_ITEM_PREFIX}\""
        [[ -n "$CCSWITCH_OP_ACCOUNT" ]] && echo "  account     = \"${CCSWITCH_OP_ACCOUNT}\""
        if [[ -n "${CCSWITCH_OP_CONNECT_HOST:-}" ]]; then
            echo "  mode        = Connect (HTTP)"
            echo "  connect_host = \"${CCSWITCH_OP_CONNECT_HOST}\""
            echo "  token       = $([ -n "${OP_CONNECT_TOKEN:-}" ] && echo "<loaded>" || echo "<missing — run ccswitch --setup-op-connect>")"
            echo "  cf_client_id     = $([ -n "${CF_ACCESS_CLIENT_ID:-}" ] && echo "<loaded>" || echo "<not set>")"
            echo "  cf_client_secret = $([ -n "${CF_ACCESS_CLIENT_SECRET:-}" ] && echo "<loaded>" || echo "<not set>")"
        else
            echo "  mode        = Signed-in CLI"
        fi
        echo ""
    fi
    if [[ "$resolved_backend" == "vault" ]] || [[ "$CCSWITCH_BACKEND" == "vault" ]]; then
        echo "[backend.vault]"
        echo "  addr  = \"${CCSWITCH_VAULT_ADDR:-<not set>}\""
        echo "  path  = \"${CCSWITCH_VAULT_PATH}\""
        echo "  token = $([ -n "$CCSWITCH_VAULT_TOKEN" ] && echo "<set>" || echo "<not set>")"
        echo ""
    fi
    echo "[sync]"
    echo "  interval = ${CCSWITCH_SYNC_INTERVAL}  # seconds"
    echo ""
    echo "[refresh]"
    echo "  expiry_buffer_minutes = ${CCSWITCH_EXPIRY_BUFFER_MINUTES}"
}

# Generate a commented TOML template at the config file location
cmd_init_config() {
    if [[ -f "$CCSWITCH_CONFIG_FILE" ]]; then
        echo "Config already exists at: $CCSWITCH_CONFIG_FILE"
        echo "Remove or move it first to regenerate."
        return 1
    fi

    mkdir -p "$(dirname "$CCSWITCH_CONFIG_FILE")"
    cat > "$CCSWITCH_CONFIG_FILE" <<'EOF'
# ccswitch configuration
# Documentation: https://github.com/zach-source/ccswitch
#
# Precedence (highest wins): env vars > this file > built-in defaults

[backend]
# Where ccswitch stores credentials.
# Options: "auto" (keychain on macOS, file on Linux), "keychain", "file",
#          "1password", "vault"
type = "auto"

# ─── 1Password backend ─────────────────────────────────────────────────────
# Two modes:
#   A. Signed-in mode — `op` CLI signed into an account (desktop app or CLI).
#      Requires biometric unlock on some operations.
#   B. Connect mode — routes through a 1Password Connect server. No biometric.
#      Set connect_host below. Token is read from macOS Keychain (or ~/.config/
#      ccswitch/connect-token on Linux). Run `ccswitch --setup-op-connect`.
[backend.onepassword]
vault = "Private"
item_prefix = "Claude Code Account"
# account = ""   # optional: op --account shorthand (signed-in mode only)

# Connect mode (takes precedence when set; `account` is ignored):
# connect_host = "https://op-connect.example.com"
# connect_token_keychain_service = "ccswitch-op-connect-token"
# connect_token_keychain_account = "ccswitch"

# ─── HashiCorp Vault / OpenBao backend ─────────────────────────────────────
# Requires: `vault` or `bao` CLI on PATH.
# Auth: token is read from config, CCSWITCH_VAULT_TOKEN env, or VAULT_TOKEN.
[backend.vault]
# addr = "https://vault.example.com"
path = "secret/data/ccswitch"
# token = ""

# ─── Sync (1Password daemon) ───────────────────────────────────────────────
[sync]
# How often the sync daemon reconciles local with 1Password (in seconds)
interval = 300

# ─── Refresh behavior ──────────────────────────────────────────────────────
[refresh]
# A token is considered "expired" this many minutes before actual expiry
expiry_buffer_minutes = 5
EOF
    chmod 600 "$CCSWITCH_CONFIG_FILE"
    echo "Created config template: $CCSWITCH_CONFIG_FILE"
    echo ""
    echo "Next steps:"
    echo "  1. Edit the file and set [backend].type to your preferred backend"
    echo "  2. Run 'ccswitch --config' to verify"
    echo "  3. For 1Password: run 'ccswitch --push' to seed the vault"
}

# Interactive setup for 1Password Connect.
# Prompts for the Connect URL + token, stores token in macOS Keychain
# (or ~/.config/ccswitch/connect-token on Linux), writes/updates TOML config,
# and verifies by listing the vault.
# Store a secret in macOS Keychain (preferred) or a 0600 file fallback.
# `-T /usr/bin/security` whitelists the security CLI so the launchd daemon
# can read without prompting. Reads the value from stdin so it never lands
# in shell history or the process arg list.
#   _store_secret <keychain_service> <keychain_account> <file_basename>
_store_secret() {
    local service="$1" account="$2" file="$3"
    local value
    IFS= read -r value
    [[ -z "$value" ]] && return 1

    if command -v security &>/dev/null; then
        security delete-generic-password -a "$account" -s "$service" &>/dev/null || true
        if ! security add-generic-password \
             -a "$account" \
             -s "$service" \
             -w "$value" \
             -U \
             -T /usr/bin/security \
             2>/dev/null; then
            echo "Error: failed to store '$service' in macOS Keychain" >&2
            return 1
        fi
    else
        local token_file="${HOME}/.config/ccswitch/${file}"
        mkdir -p "$(dirname "$token_file")"
        printf '%s' "$value" > "$token_file"
        chmod 600 "$token_file"
    fi
}

# Interactive setup for 1Password Connect (Cloudflare Access–fronted).
# Captures three secrets:
#   - Connect bearer token (X-OP-Token, kept in keychain svc ccswitch-op-connect-token)
#   - CF Access client ID  (CF-Access-Client-Id,  kept in keychain svc ccswitch-cf-access-client-id)
#   - CF Access client secret (CF-Access-Client-Secret, kept in keychain svc ccswitch-cf-access-client-secret)
#
# Secrets can either be entered manually or bootstrapped from 1Password
# references (one-time use of `op read` — the values are piped straight to
# keychain storage so they never appear in shell history or the TTY).
cmd_setup_op_connect() {
    echo "Setting up 1Password Connect for ccswitch"
    echo ""

    # URL prompt
    local current_host="${CCSWITCH_OP_CONNECT_HOST:-${OP_CONNECT_HOST:-}}"
    local default_prompt=""
    [[ -n "$current_host" ]] && default_prompt=" [$current_host]"
    local connect_host=""
    read -r -p "Connect server URL${default_prompt}: " connect_host
    [[ -z "$connect_host" ]] && connect_host="$current_host"
    if [[ -z "$connect_host" ]]; then
        echo "Error: Connect URL required" >&2
        return 1
    fi
    if [[ ! "$connect_host" =~ ^https?:// ]]; then
        echo "Error: URL must start with http:// or https://" >&2
        return 1
    fi

    # Secret source
    echo ""
    echo "Secret source:"
    echo "  1) Enter tokens manually (3 prompts)"
    echo "  2) Read from 1Password references (via signed-in op CLI, one-time)"
    local choice=""
    read -r -p "Choose [1/2] (default 2): " choice
    [[ -z "$choice" ]] && choice=2

    local cf_needed=""
    read -r -p "Is the Connect server behind Cloudflare Access? [y/N]: " cf_needed
    local want_cf=0
    [[ "$cf_needed" =~ ^[Yy] ]] && want_cf=1

    local kc_token_svc="${CCSWITCH_OP_CONNECT_TOKEN_KEYCHAIN_SERVICE}"
    local kc_account="${CCSWITCH_OP_CONNECT_TOKEN_KEYCHAIN_ACCOUNT}"
    local kc_cf_id_svc="$CCSWITCH_KC_CF_ID_SVC"
    local kc_cf_secret_svc="$CCSWITCH_KC_CF_SECRET_SVC"

    if [[ "$choice" == "2" ]]; then
        if ! command -v op &>/dev/null; then
            echo "Error: op CLI not installed; choose option 1 instead." >&2
            return 1
        fi

        local op_account=""
        read -r -p "1Password account shorthand (e.g. my.1password.com, empty for default): " op_account
        local -a acct_args=()
        [[ -n "$op_account" ]] && acct_args=(--account "$op_account")

        local ref_tok ref_cid ref_csec
        read -r -p "Reference for Connect bearer token [op://Personal Agents/op-connect-token/credential]: " ref_tok
        ref_tok="${ref_tok:-op://Personal Agents/op-connect-token/credential}"
        if [[ $want_cf -eq 1 ]]; then
            read -r -p "Reference for CF Access client id [op://Personal Agents/op-connect-cf-access/client_id]: " ref_cid
            ref_cid="${ref_cid:-op://Personal Agents/op-connect-cf-access/client_id}"
            read -r -p "Reference for CF Access client secret [op://Personal Agents/op-connect-cf-access/client_secret]: " ref_csec
            ref_csec="${ref_csec:-op://Personal Agents/op-connect-cf-access/client_secret}"
        fi

        # Drive the three secrets from a single tuple list. Each entry:
        #   label|svc|file|ref|required(0|1 — 1 means always, else CF-only)
        # Pipe `op read` output straight into _store_secret via stdin so
        # values never land in shell variables or command args.
        local row label svc file ref required
        local -a secrets=(
            "Connect bearer token|$kc_token_svc|connect-token|$ref_tok|1"
            "CF Access client id|$kc_cf_id_svc|cf-access-client-id|$ref_cid|0"
            "CF Access client secret|$kc_cf_secret_svc|cf-access-client-secret|$ref_csec|0"
        )
        for row in "${secrets[@]}"; do
            IFS='|' read -r label svc file ref required <<<"$row"
            [[ "$required" == "0" && $want_cf -eq 0 ]] && continue
            echo "Storing ${label}..."
            if ! op read "${acct_args[@]}" "$ref" \
                 | _store_secret "$svc" "$kc_account" "$file"; then
                echo "Error: failed to read or store ${label}" >&2
                return 1
            fi
        done
    else
        # Manual entry — hidden prompts. Same tuple shape minus the op:// ref.
        local row label svc file required tmp=""
        local -a secrets=(
            "Connect bearer token|$kc_token_svc|connect-token|1"
            "CF Access client id|$kc_cf_id_svc|cf-access-client-id|0"
            "CF Access client secret|$kc_cf_secret_svc|cf-access-client-secret|0"
        )
        for row in "${secrets[@]}"; do
            IFS='|' read -r label svc file required <<<"$row"
            [[ "$required" == "0" && $want_cf -eq 0 ]] && continue
            read -r -s -p "${label} (hidden): " tmp; echo
            if ! printf '%s' "$tmp" | _store_secret "$svc" "$kc_account" "$file"; then
                echo "Error: failed to store ${label}" >&2
                unset tmp
                return 1
            fi
            unset tmp
        done
    fi

    echo "Secrets stored."

    # Write/update TOML config with connect_host
    mkdir -p "$(dirname "$CCSWITCH_CONFIG_FILE")"
    if [[ ! -f "$CCSWITCH_CONFIG_FILE" ]]; then
        cmd_init_config >/dev/null
    fi
    local patch_ok=1
    python3 - "$CCSWITCH_CONFIG_FILE" "$connect_host" "$kc_token_svc" "$kc_account" <<'PYEOF'
import sys, re, pathlib

path, host, service, account = sys.argv[1:5]
text = pathlib.Path(path).read_text()

if re.search(r'(?m)^\s*type\s*=\s*"[^"]*"\s*$', text):
    text = re.sub(r'(?m)^\s*type\s*=\s*"[^"]*"\s*$', 'type = "1password"', text, count=1)
else:
    text = '[backend]\ntype = "1password"\n\n' + text

pattern = re.compile(r'(\[backend\.onepassword\][^\[]*)', re.DOTALL)
m = pattern.search(text)
new_lines = [
    f'connect_host = "{host}"',
    f'connect_token_keychain_service = "{service}"',
    f'connect_token_keychain_account = "{account}"',
]
if m:
    block = m.group(1)
    block = re.sub(r'(?m)^\s*#?\s*connect_(host|token_keychain_service|token_keychain_account)\s*=.*$\n?', '', block)
    if not block.endswith('\n'):
        block += '\n'
    block += '\n'.join(new_lines) + '\n'
    text = text[:m.start(1)] + block + text[m.end(1):]
else:
    text += '\n[backend.onepassword]\n' + '\n'.join(new_lines) + '\n'

pathlib.Path(path).write_text(text)
PYEOF
    patch_ok=$?
    if [[ $patch_ok -ne 0 ]]; then
        echo "Warning: could not auto-update TOML. Edit ${CCSWITCH_CONFIG_FILE} manually." >&2
    fi

    # Reload so subsequent calls see the new config
    unset OP_CONNECT_HOST OP_CONNECT_TOKEN CF_ACCESS_CLIENT_ID CF_ACCESS_CLIENT_SECRET CCSWITCH_OP_CONNECT_HOST
    _load_config

    # Verify via HTTP — never shells out to op CLI in Connect mode
    echo ""
    echo "Verifying Connect access..."
    if _op_check; then
        echo "OK — Connect reachable, headers valid."
        echo ""
        echo "Vault list:"
        _op_connect_api GET /v1/vaults | jq -r '.[] | "  " + .name' || true
    else
        echo ""
        echo "Setup stored, but verification failed. Check URL/token/CF Access secrets and server reachability." >&2
        return 1
    fi
}

# Push: Push local credentials + sequence.json to 1Password.
# Overwrites 1Password with local state.
cmd_push() {
    _op_check || return 1
    [[ ! -f "$SEQUENCE_FILE" ]] && { echo "No local sequence file"; return 1; }

    echo "Pushing credentials to 1Password..."
    echo "  Vault: ${CCSWITCH_OP_VAULT}"
    echo ""

    local ids
    ids=$(jq -r '.sequence[]' "$SEQUENCE_FILE")
    local active_id
    active_id=$(jq -r '.activeAccountId' "$SEQUENCE_FILE")

    # Save current backend, push via 1password backend directly
    local saved_backend="${CCSWITCH_BACKEND}"

    local pushed=0 skipped=0
    for id in $ids; do
        local email
        email=$(get_account_email "$id")
        local cred_json

        # Read from current backend (not forced to 1password)
        if [[ "$id" == "$active_id" ]]; then
            cred_json=$(CCSWITCH_BACKEND="$saved_backend" read_credentials)
        else
            cred_json=$(CCSWITCH_BACKEND="$saved_backend" read_account_credentials "$id" "$email")
        fi

        if [[ -z "$cred_json" ]]; then
            echo "  ${id} ${email}: no local credentials, skipping"
            skipped=$((skipped + 1))
            continue
        fi

        # Push to 1Password
        if _op_write "$(_op_item_name "${id}-${email}")" "$cred_json"; then
            local exp_ms
            exp_ms=$(_cred_expires_ms "$cred_json")
            local hours
            hours=$(_cred_hours_left "$exp_ms")
            echo "  ${id} ${email}: pushed (expires in ${hours}h)"
            pushed=$((pushed + 1))
        else
            echo "  ${id} ${email}: push failed"
        fi
    done

    # Push sequence.json (without switchLog to keep size reasonable)
    echo ""
    echo -n "Pushing sequence metadata... "
    local seq_content
    seq_content=$(jq 'del(.switchLog)' "$SEQUENCE_FILE")
    if _op_write "$(_op_sequence_item_name)" "$seq_content"; then
        echo "✓"
    else
        echo "✗"
    fi

    echo ""
    echo "Summary: ${pushed} pushed, ${skipped} skipped"
}

# Pull: Pull credentials + sequence.json from 1Password to local.
# Overwrites local with 1Password state.
cmd_pull() {
    _op_check || return 1

    echo "Pulling credentials from 1Password..."
    echo "  Vault: ${CCSWITCH_OP_VAULT}"
    echo ""

    # Pull sequence.json first
    local remote_seq
    remote_seq=$(_op_read "$(_op_sequence_item_name)")
    if [[ -z "$remote_seq" ]]; then
        echo "Error: No sequence item found in 1Password (expected: $(_op_sequence_item_name))"
        echo "Run 'ccswitch --push' first from a machine with credentials."
        return 1
    fi

    # Backup existing sequence before overwriting
    if [[ -f "$SEQUENCE_FILE" ]]; then
        cp "$SEQUENCE_FILE" "${SEQUENCE_FILE}.pre-pull"
    else
        setup_directories
    fi

    # Preserve local switchLog if it exists
    local local_switchlog
    local_switchlog=$(jq -c '.switchLog // []' "$SEQUENCE_FILE" 2>/dev/null || echo "[]")

    echo "$remote_seq" | jq --argjson log "$local_switchlog" '.switchLog = $log' > "$SEQUENCE_FILE"
    echo "Pulled sequence metadata ✓"
    echo ""

    # Now pull each account's credentials
    local ids
    ids=$(jq -r '.sequence[]' "$SEQUENCE_FILE")
    local active_id
    active_id=$(jq -r '.activeAccountId' "$SEQUENCE_FILE")

    local pulled=0 skipped=0
    for id in $ids; do
        local email
        email=$(get_account_email "$id")
        local remote_cred
        remote_cred=$(_op_read "$(_op_item_name "${id}-${email}")")

        if [[ -z "$remote_cred" ]]; then
            echo "  ${id} ${email}: not in 1Password, skipping"
            skipped=$((skipped + 1))
            continue
        fi

        # Write to current backend
        if [[ "$id" == "$active_id" ]]; then
            write_credentials "$remote_cred"
        fi
        write_account_credentials "$id" "$email" "$remote_cred"

        local exp_ms hours
        exp_ms=$(_cred_expires_ms "$remote_cred")
        hours=$(_cred_hours_left "$exp_ms")
        echo "  ${id} ${email}: pulled (expires in ${hours}h)"
        pulled=$((pulled + 1))
    done

    echo ""
    echo "Summary: ${pulled} pulled, ${skipped} skipped"
}

# Sync: Bi-directional sync between local and 1Password.
# For each account, keep the credential with the later expiresAt.
# Used by the daemon for continuous sync.
cmd_sync() {
    local quiet="false"
    [[ "${1:-}" == "--quiet" ]] && quiet="true"

    _op_check || return 1
    [[ ! -f "$SEQUENCE_FILE" ]] && { $quiet || echo "No local sequence file"; return 1; }

    local log_fn
    if $quiet; then
        log_fn() { :; }
    else
        log_fn() { echo "  $*"; }
    fi

    $quiet || echo "Syncing with 1Password (vault: ${CCSWITCH_OP_VAULT})..."

    # Sync sequence.json: newest lastUpdated wins
    local remote_seq local_updated remote_updated
    remote_seq=$(_op_read "$(_op_sequence_item_name)")

    if [[ -z "$remote_seq" ]]; then
        # No remote state — push our local state
        log_fn "No remote sequence; pushing local state"
        local seq_content
        seq_content=$(jq 'del(.switchLog)' "$SEQUENCE_FILE")
        _op_write "$(_op_sequence_item_name)" "$seq_content"
    else
        local_updated=$(jq -r '.lastUpdated // ""' "$SEQUENCE_FILE")
        remote_updated=$(echo "$remote_seq" | jq -r '.lastUpdated // ""')

        if [[ "$remote_updated" > "$local_updated" ]]; then
            # Remote is newer — merge accounts (preserve local switchLog)
            log_fn "Remote sequence newer (${remote_updated} > ${local_updated}); pulling"
            local local_switchlog
            local_switchlog=$(jq -c '.switchLog // []' "$SEQUENCE_FILE")
            echo "$remote_seq" | jq --argjson log "$local_switchlog" '.switchLog = $log' > "$SEQUENCE_FILE"
        elif [[ "$local_updated" > "$remote_updated" ]]; then
            log_fn "Local sequence newer (${local_updated} > ${remote_updated}); pushing"
            local seq_content
            seq_content=$(jq 'del(.switchLog)' "$SEQUENCE_FILE")
            _op_write "$(_op_sequence_item_name)" "$seq_content"
        fi
    fi

    # Sync each account's credentials: newest expiresAt wins
    local ids
    ids=$(jq -r '.sequence[]' "$SEQUENCE_FILE")
    local active_id
    active_id=$(jq -r '.activeAccountId' "$SEQUENCE_FILE")

    local pushed=0 pulled=0 nop=0
    for id in $ids; do
        local email
        email=$(get_account_email "$id")

        local local_cred remote_cred
        if [[ "$id" == "$active_id" ]]; then
            local_cred=$(read_credentials)
        else
            local_cred=$(read_account_credentials "$id" "$email")
        fi
        remote_cred=$(_op_read "$(_op_item_name "${id}-${email}")")

        local local_exp remote_exp
        local_exp=$(_cred_expires_ms "$local_cred")
        remote_exp=$(_cred_expires_ms "$remote_cred")

        if [[ -n "$local_cred" ]] && [[ -z "$remote_cred" ]]; then
            # Local only — push
            _op_write "$(_op_item_name "${id}-${email}")" "$local_cred"
            log_fn "${id} ${email}: pushed (new in 1Password)"
            pushed=$((pushed + 1))
        elif [[ -z "$local_cred" ]] && [[ -n "$remote_cred" ]]; then
            # Remote only — pull
            if [[ "$id" == "$active_id" ]]; then
                write_credentials "$remote_cred"
            fi
            write_account_credentials "$id" "$email" "$remote_cred"
            log_fn "${id} ${email}: pulled (new locally)"
            pulled=$((pulled + 1))
        elif [[ "$local_exp" -gt "$remote_exp" ]]; then
            # Local is newer — push
            _op_write "$(_op_item_name "${id}-${email}")" "$local_cred"
            log_fn "${id} ${email}: pushed (local fresher)"
            pushed=$((pushed + 1))
        elif [[ "$remote_exp" -gt "$local_exp" ]]; then
            # Remote is newer — pull
            if [[ "$id" == "$active_id" ]]; then
                write_credentials "$remote_cred"
            fi
            write_account_credentials "$id" "$email" "$remote_cred"
            log_fn "${id} ${email}: pulled (remote fresher)"
            pulled=$((pulled + 1))
        else
            nop=$((nop + 1))
        fi
    done

    $quiet || echo "Sync complete: ${pushed} pushed, ${pulled} pulled, ${nop} unchanged"
}

# Daemon: Run sync continuously in background.
# Intended to be launched via launchd (macOS) or systemd user unit (Linux).
cmd_daemon() {
    local interval="${CCSWITCH_SYNC_INTERVAL:-300}"  # 5 min default
    local log_file="${CCSWITCH_DAEMON_LOG:-$HOME/.claude-switch-backup/daemon.log}"

    echo "ccswitch sync daemon starting (interval: ${interval}s, log: ${log_file})"
    mkdir -p "$(dirname "$log_file")"

    # Graceful shutdown
    trap 'echo "[$(date -u +%FT%TZ)] daemon stopped" >> "$log_file"; exit 0' TERM INT

    while true; do
        {
            echo "[$(date -u +%FT%TZ)] sync tick"
            cmd_sync 2>&1
        } >> "$log_file" 2>&1

        sleep "$interval"
    done
}

# Add account
cmd_add_account() {
    setup_directories
    init_sequence_file
    
    local current_email
    current_email=$(get_current_account)
    
    if [[ "$current_email" == "none" ]]; then
        echo "Error: No active Claude account found. Please log in first."
        exit 1
    fi
    
    if account_exists "$current_email"; then
        echo "Account $current_email is already managed."
        exit 0
    fi
    
    local account_id
    account_id=$(hash_email "$current_email")

    # Backup current credentials and config
    local current_creds current_config
    current_creds=$(read_credentials)
    current_config=$(cat "$(get_claude_config_path)")

    if [[ -z "$current_creds" ]]; then
        echo "Error: No credentials found for current account"
        exit 1
    fi

    # Get account UUID
    local account_uuid
    account_uuid=$(jq -r '.oauthAccount.accountUuid' "$(get_claude_config_path)")

    # Store backups
    write_account_credentials "$account_id" "$current_email" "$current_creds"
    write_account_config "$account_id" "$current_email" "$current_config"

    # Update sequence.json
    local updated_sequence
    updated_sequence=$(jq --arg id "$account_id" --arg email "$current_email" --arg uuid "$account_uuid" --arg now "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '
        .accounts[$id] = {
            email: $email,
            uuid: $uuid,
            added: $now
        } |
        .sequence += [$id] |
        .activeAccountId = $id |
        .lastUpdated = $now
    ' "$SEQUENCE_FILE")

    write_json "$SEQUENCE_FILE" "$updated_sequence"

    echo "Added account ${account_id}: ${current_email}"
}

# Remove account
cmd_remove_account() {
    if [[ $# -eq 0 ]]; then
        echo "Usage: $0 --remove-account <account_number|email>"
        exit 1
    fi
    
    local identifier="$1"
    local account_id

    if [[ ! -f "$SEQUENCE_FILE" ]]; then
        echo "Error: No accounts are managed yet"
        exit 1
    fi

    account_id=$(resolve_account_identifier "$identifier")
    if [[ -z "$account_id" ]]; then
        echo "Error: No account found matching: $identifier"
        exit 1
    fi

    local email
    email=$(get_account_email "$account_id")

    local active_account
    active_account=$(jq -r '.activeAccountId' "$SEQUENCE_FILE")

    if [[ "$active_account" == "$account_id" ]]; then
        echo "Warning: Account ${account_id} (${email}) is currently active"
    fi

    echo -n "Are you sure you want to permanently remove ${account_id} (${email})? [y/N] "
    read -r confirm

    if [[ "$confirm" != "y" && "$confirm" != "Y" ]]; then
        echo "Cancelled"
        exit 0
    fi

    # Remove backup credentials via backend
    delete_account_credentials "$account_id" "$email"
    rm -f "$BACKUP_DIR/configs/.claude-config-${account_id}-${email}.json"

    # Update sequence.json
    local updated_sequence
    updated_sequence=$(jq --arg id "$account_id" --arg now "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '
        del(.accounts[$id]) |
        .sequence = (.sequence | map(select(. != $id))) |
        .lastUpdated = $now
    ' "$SEQUENCE_FILE")

    write_json "$SEQUENCE_FILE" "$updated_sequence"

    echo "Removed ${account_id} (${email})"
}

# First-run setup workflow
first_run_setup() {
    local current_email
    current_email=$(get_current_account)
    
    if [[ "$current_email" == "none" ]]; then
        echo "No active Claude account found. Please log in first."
        return 1
    fi
    
    echo -n "No managed accounts found. Add current account ($current_email) to managed list? [Y/n] "
    read -r response
    
    if [[ "$response" == "n" || "$response" == "N" ]]; then
        echo "Setup cancelled. You can run '$0 --add-account' later."
        return 1
    fi
    
    cmd_add_account
    return 0
}

# Get org name from backup config
get_account_org_name() {
    local account_num="$1"
    local email="$2"
    local config_file="$BACKUP_DIR/configs/.claude-config-${account_num}-${email}.json"

    if [[ -f "$config_file" ]]; then
        local org_name
        org_name=$(jq -r '.oauthAccount.organizationName // "Personal"' "$config_file" 2>/dev/null)
        # Simplify org name display
        if [[ "$org_name" == *"'s Organization" ]]; then
            org_name="Personal"
        fi
        echo "$org_name"
    else
        echo "Unknown"
    fi
}

# List accounts
cmd_list() {
    if [[ ! -f "$SEQUENCE_FILE" ]]; then
        echo "No accounts are managed yet."
        first_run_setup
        exit 0
    fi

    # Get current active account from .claude.json
    local current_email current_org
    current_email=$(get_current_account)

    # Get current org name
    current_org=$(jq -r '.oauthAccount.organizationName // "Personal"' "$(get_claude_config_path)" 2>/dev/null)
    if [[ "$current_org" == *"'s Organization" ]]; then
        current_org="Personal"
    fi

    # Find which account ID corresponds to the current email
    local active_account_id=""
    if [[ "$current_email" != "none" ]]; then
        active_account_id=$(hash_email "$current_email")
    fi

    echo "Accounts:"

    local sequence_ids
    sequence_ids=$(jq -r '.sequence[]' "$SEQUENCE_FILE")

    while IFS= read -r id; do
        local email org_name
        email=$(jq -r --arg id "$id" '.accounts[$id].email' "$SEQUENCE_FILE")

        if [[ "$id" == "$active_account_id" ]]; then
            org_name="$current_org"
            echo "  ${id}  ${email}  [${org_name}] (active)"
        else
            org_name=$(get_account_org_name "$id" "$email")
            echo "  ${id}  ${email}  [${org_name}]"
        fi
    done <<< "$sequence_ids"
}

# Switch to next account
cmd_switch() {
    if [[ ! -f "$SEQUENCE_FILE" ]]; then
        echo "Error: No accounts are managed yet"
        exit 1
    fi
    
    local current_email
    current_email=$(get_current_account)
    
    if [[ "$current_email" == "none" ]]; then
        echo "Error: No active Claude account found"
        exit 1
    fi
    
    # Check if current account is managed
    if ! account_exists "$current_email"; then
        echo "Notice: Active account '$current_email' was not managed."
        cmd_add_account
        local account_id
        account_id=$(jq -r '.activeAccountId' "$SEQUENCE_FILE")
        echo "It has been automatically added as ${account_id}."
        echo "Please run 'ccswitch --switch' again to switch to the next account."
        exit 0
    fi

    local active_account sequence
    active_account=$(jq -r '.activeAccountId' "$SEQUENCE_FILE")
    sequence=($(jq -r '.sequence[]' "$SEQUENCE_FILE"))

    # Find next account in sequence
    local next_account current_index=0
    for i in "${!sequence[@]}"; do
        if [[ "${sequence[i]}" == "$active_account" ]]; then
            current_index=$i
            break
        fi
    done

    next_account="${sequence[$(((current_index + 1) % ${#sequence[@]}))]}"

    perform_switch "$next_account"
}

# Switch to specific account (accepts hash, email, or numeric index)
cmd_switch_to() {
    if [[ $# -eq 0 ]]; then
        echo "Usage: $0 --switch-to <hash|email|index>"
        exit 1
    fi

    local identifier="$1"

    if [[ ! -f "$SEQUENCE_FILE" ]]; then
        echo "Error: No accounts are managed yet"
        exit 1
    fi

    local target_account
    target_account=$(resolve_account_identifier "$identifier")

    if [[ -z "$target_account" ]]; then
        echo "Error: No account found matching: $identifier"
        exit 1
    fi

    perform_switch "$target_account"
}

# Perform the actual account switch (target is an account hash ID)
perform_switch() {
    local target_id="$1"

    local current_id target_email current_email
    current_id=$(jq -r '.activeAccountId' "$SEQUENCE_FILE")
    target_email=$(get_account_email "$target_id")
    current_email=$(get_current_account)

    # Step 1: Backup current account
    local current_creds current_config
    current_creds=$(read_credentials)
    current_config=$(cat "$(get_claude_config_path)")

    write_account_credentials "$current_id" "$current_email" "$current_creds"
    write_account_config "$current_id" "$current_email" "$current_config"

    # Step 2: Retrieve target account
    local target_creds target_config
    target_creds=$(read_account_credentials "$target_id" "$target_email")
    target_config=$(read_account_config "$target_id" "$target_email")

    if [[ -z "$target_creds" || -z "$target_config" ]]; then
        echo "Error: Missing backup data for ${target_id} (${target_email})"
        exit 1
    fi

    # Auto-refresh expired target token before switching
    if _token_is_expired "$target_creds"; then
        echo "Token expired for ${target_id}, refreshing..."
        if refresh_account_token "$target_id"; then
            target_creds=$(read_account_credentials "$target_id" "$target_email")
            echo "Token refreshed ✓"
        else
            echo "Warning: Token refresh failed. Switch may require re-authentication."
        fi
    fi

    # Step 3: Activate target account
    write_credentials "$target_creds"

    # Extract oauthAccount from backup and validate
    local oauth_section
    oauth_section=$(echo "$target_config" | jq '.oauthAccount' 2>/dev/null)
    if [[ -z "$oauth_section" || "$oauth_section" == "null" ]]; then
        echo "Error: Invalid oauthAccount in backup"
        exit 1
    fi

    local merged_config
    merged_config=$(jq --argjson oauth "$oauth_section" '.oauthAccount = $oauth' "$(get_claude_config_path)" 2>/dev/null)
    if [[ $? -ne 0 ]]; then
        echo "Error: Failed to merge config"
        exit 1
    fi

    write_json "$(get_claude_config_path)" "$merged_config"

    # Step 4: Update state (track activation times per account for usage attribution)
    local updated_sequence
    updated_sequence=$(jq --arg id "$target_id" --arg prev "$current_id" --arg now "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '
        .activeAccountId = $id |
        .lastUpdated = $now |
        .accounts[$id].activeSince = $now |
        (if $prev != "null" and $prev != "" then .accounts[$prev].lastDeactivated = $now else . end) |
        .switchLog = ((.switchLog // []) + [{"from": $prev, "to": $id, "at": $now}]) |
        .switchLog = (.switchLog | if length > 100 then .[-100:] else . end)
    ' "$SEQUENCE_FILE")

    write_json "$SEQUENCE_FILE" "$updated_sequence"

    echo "Switched to ${target_id} (${target_email})"
    # Display updated account list
    cmd_list
    echo ""
    echo "Please restart Claude Code to use the new authentication."
    echo ""
    
}

# Show current account
cmd_current() {
    local settings_path
    settings_path=$(get_settings_path)

    # Check if z.ai is configured
    if [[ -f "$settings_path" ]]; then
        local base_url
        base_url=$(jq -r '.env.ANTHROPIC_BASE_URL // empty' "$settings_path" 2>/dev/null)

        if [[ -n "$base_url" && "$base_url" == *"z.ai"* ]]; then
            echo "ztaylor@stigen.ai (z.ai API)"
            return 0
        fi
    fi

    # Otherwise show OAuth account
    local current_email
    current_email=$(get_current_account)

    if [[ "$current_email" == "none" ]]; then
        echo "No active Claude account"
        return 1
    fi

    local org_name
    org_name=$(jq -r '.oauthAccount.organizationName // "Personal"' "$(get_claude_config_path)" 2>/dev/null)

    # Simplify org name display
    if [[ "$org_name" == *"'s Organization" ]]; then
        org_name="Personal"
    fi

    echo "$current_email ($org_name)"
}

# Get Claude settings.json path
get_settings_path() {
    echo "$HOME/.claude/settings.json"
}

# Switch to z.ai API endpoint (ztaylor@stigen.ai account)
cmd_use_zai() {
    local settings_path
    settings_path=$(get_settings_path)

    echo "Configuring Claude to use z.ai API (ztaylor@stigen.ai)..."

    # Fetch token from cached secret (prefers keychain, falls back to 1Password)
    local zai_token

    if [[ -x "$HOME/.claude/scripts/mcp-secret-cache.sh" ]]; then
        echo "Fetching z.ai token from keychain cache..."
        zai_token=$("$HOME/.claude/scripts/mcp-secret-cache.sh" get zai ZAI_API_TOKEN 2>/dev/null)
    fi

    # Fallback to direct op read if cache not available
    if [[ -z "$zai_token" ]] && command -v op >/dev/null 2>&1; then
        echo "Fetching z.ai token from 1Password..."
        zai_token=$(op read --account=S43LKCIJPNGYLE52ZXH2MM7LJA "op://Employee/bzdhsxie4x5emfkacyiwtyc6bi/credential" 2>/dev/null)
    fi

    if [[ -z "$zai_token" ]]; then
        echo "Error: Failed to fetch z.ai token"
        echo "Make sure you're signed into the correct 1Password account"
        echo "Or initialize the cache: mcp-secret-cache refresh zai"
        exit 1
    fi

    # Create or update settings.json with z.ai env vars
    local new_settings
    if [[ -f "$settings_path" ]]; then
        # Merge with existing settings
        new_settings=$(jq --arg token "$zai_token" '
            .env = {
                "ANTHROPIC_AUTH_TOKEN": $token,
                "ANTHROPIC_BASE_URL": "https://api.z.ai/api/anthropic",
                "API_TIMEOUT_MS": "3000000"
            }
        ' "$settings_path")
    else
        # Create new settings file
        new_settings=$(jq -n --arg token "$zai_token" '{
            "env": {
                "ANTHROPIC_AUTH_TOKEN": $token,
                "ANTHROPIC_BASE_URL": "https://api.z.ai/api/anthropic",
                "API_TIMEOUT_MS": "3000000"
            }
        }')
    fi

    # Ensure directory exists
    mkdir -p "$(dirname "$settings_path")"

    # Write settings
    write_json "$settings_path" "$new_settings"

    echo "✓ Configured Claude to use z.ai API"
    echo "  Base URL: https://api.z.ai/api/anthropic"
    echo "  Timeout: 3000000ms (50 min)"
    echo ""
    echo "Please restart Claude Code to use the new configuration."
}

# Clear z.ai configuration and revert to default Anthropic API
cmd_use_anthropic() {
    local settings_path
    settings_path=$(get_settings_path)

    if [[ ! -f "$settings_path" ]]; then
        echo "No settings.json found - already using default Anthropic API"
        exit 0
    fi

    # Check if z.ai env vars are set
    local has_zai_env
    has_zai_env=$(jq -e '.env.ANTHROPIC_BASE_URL // empty' "$settings_path" 2>/dev/null)

    if [[ -z "$has_zai_env" ]]; then
        echo "Already using default Anthropic API (no custom env vars set)"
        exit 0
    fi

    echo "Removing z.ai configuration and reverting to default Anthropic API..."

    # Remove env section from settings
    local new_settings
    new_settings=$(jq 'del(.env)' "$settings_path")

    write_json "$settings_path" "$new_settings"

    echo "✓ Reverted to default Anthropic API"
    echo ""
    echo "Please restart Claude Code to use the new configuration."
}

# Show current API configuration
cmd_api_status() {
    local settings_path
    settings_path=$(get_settings_path)

    if [[ ! -f "$settings_path" ]]; then
        echo "API: Default Anthropic API"
        echo "Settings: No custom settings.json"
        return
    fi

    local base_url
    base_url=$(jq -r '.env.ANTHROPIC_BASE_URL // empty' "$settings_path" 2>/dev/null)

    if [[ -n "$base_url" ]]; then
        echo "API: Custom endpoint"
        echo "  Base URL: $base_url"
        if [[ "$base_url" == *"z.ai"* ]]; then
            echo "  Account: ztaylor@stigen.ai (z.ai)"
        fi
        local timeout
        timeout=$(jq -r '.env.API_TIMEOUT_MS // empty' "$settings_path" 2>/dev/null)
        if [[ -n "$timeout" ]]; then
            echo "  Timeout: ${timeout}ms"
        fi
        local has_token
        has_token=$(jq -e '.env.ANTHROPIC_AUTH_TOKEN // empty' "$settings_path" 2>/dev/null)
        if [[ -n "$has_token" ]]; then
            echo "  Auth Token: ✓ configured"
        fi
    else
        echo "API: Default Anthropic API"
    fi
}

# Show 5h block and weekly usage limits with percentages
cmd_usage() {
    if ! command -v ccusage &>/dev/null; then
        echo "ccusage not installed"
        return 1
    fi

    # Get current account and its weekly token limit from sequence.json
    local account weekly_limit
    account=$(cmd_current 2>/dev/null || echo "unknown")
    weekly_limit=$(python3 -c "
import json, sys
try:
    seq = json.load(open('$SEQUENCE_FILE'))
    active = str(seq.get('activeAccountId', 1))
    acct = seq.get('accounts', {}).get(active, {})
    print(acct.get('weeklyTokenLimit', 0))
except: print(0)
" 2>/dev/null)

    # Get block data with token limits
    local block_json weekly_json
    block_json=$(ccusage blocks --active --json --offline --token-limit max 2>/dev/null || echo '{}')
    weekly_json=$(ccusage weekly --json --offline 2>/dev/null || echo '{}')

    # Render everything in one python call
    python3 -c "
import sys, json
from datetime import datetime, timezone, timedelta

block_data = json.loads('''$block_json''')
weekly_data = json.loads('''$weekly_json''')
account = '''$account'''
weekly_limit = int('''$weekly_limit''' or '0')

BOLD = '\033[1m'
DIM = '\033[0;90m'
CYAN = '\033[0;36m'
GREEN = '\033[0;32m'
YELLOW = '\033[0;33m'
RED = '\033[0;31m'
NC = '\033[0m'

def fmt_tokens(t):
    if t >= 1_000_000: return f'{t/1_000_000:.1f}M'
    elif t >= 1_000: return f'{t/1_000:.0f}K'
    return str(t)

def pct_color(pct):
    if pct >= 80: return RED
    if pct >= 50: return YELLOW
    return GREEN

def bar(pct, width=20):
    filled = int(pct / 100 * width)
    return '█' * filled + '░' * (width - filled)

print(f'{BOLD}Claude Code Usage{NC}')
print(f'{DIM}Account:{NC} {CYAN}{account}{NC}')
print()

# ── 5-Hour Block ──
blocks = block_data.get('blocks', [])
if blocks:
    b = blocks[0]
    tokens = b.get('totalTokens', 0)
    cost = b.get('costUSD', 0)
    burn = b.get('burnRate', {})
    tls = b.get('tokenLimitStatus', {})
    block_limit = tls.get('limit', 0)
    block_pct = tls.get('percentUsed', 0)
    status = tls.get('status', 'ok')

    try:
        start_dt = datetime.fromisoformat(b['startTime'].replace('Z', '+00:00'))
        end_dt = datetime.fromisoformat(b['endTime'].replace('Z', '+00:00'))
        now = datetime.now(timezone.utc)
        remaining = max(0, (end_dt - now).total_seconds() / 60)
        remaining_h, remaining_m = int(remaining // 60), int(remaining % 60)
    except:
        remaining_h, remaining_m = 0, 0

    cost_per_h = burn.get('costPerHour', 0)
    c = pct_color(block_pct)

    print(f'{BOLD}5-Hour Block{NC}')
    print(f'  {c}{bar(block_pct)} {block_pct:.0f}%{NC}  ({fmt_tokens(tokens)} / {fmt_tokens(block_limit)})')
    print(f'  Cost: \${cost:.2f} (\${cost_per_h:.2f}/hr)  Time left: {remaining_h}h {remaining_m}m')
else:
    print(f'{BOLD}5-Hour Block{NC}')
    print(f'  No active block')

print()

# ── Weekly Usage ──
weeks = weekly_data.get('weekly', [])
now = datetime.now(timezone.utc)
week_start = (now - timedelta(days=now.weekday())).strftime('%Y-%m-%d')

this_week = None
last_week = None
for w in weeks:
    if w.get('week', '') >= week_start:
        this_week = w
    elif this_week is None:
        last_week = w
if not this_week and weeks:
    this_week = weeks[-1]
if not last_week and len(weeks) >= 2:
    last_week = weeks[-2]

print(f'{BOLD}Weekly Usage{NC}')

if this_week:
    tw_tokens = this_week.get('totalTokens', 0)
    tw_cost = this_week.get('totalCost', 0)
    models = this_week.get('modelsUsed', [])
    short_models = [m.replace('claude-', '').replace('-20251001', '').replace('-20251101', '') for m in models]

    if weekly_limit > 0:
        tw_pct = min(100, tw_tokens / weekly_limit * 100)
        c = pct_color(tw_pct)
        print(f'  This week:  {c}{bar(tw_pct)} {tw_pct:.0f}%{NC}  ({fmt_tokens(tw_tokens)} / {fmt_tokens(weekly_limit)})')
    else:
        print(f'  This week:  {fmt_tokens(tw_tokens)} tokens')
    print(f'  Cost: \${tw_cost:.2f}  Models: {\", \".join(short_models)}')
else:
    print(f'  This week:  No data')

if last_week:
    lw_tokens = last_week.get('totalTokens', 0)
    lw_cost = last_week.get('totalCost', 0)
    if weekly_limit > 0:
        lw_pct = min(100, lw_tokens / weekly_limit * 100)
        c = pct_color(lw_pct)
        print(f'  Last week:  {c}{bar(lw_pct)} {lw_pct:.0f}%{NC}  ({fmt_tokens(lw_tokens)} / {fmt_tokens(weekly_limit)})')
    else:
        print(f'  Last week:  {fmt_tokens(lw_tokens)} tokens  \${lw_cost:.2f}')

if weekly_limit == 0:
    print(f'{DIM}  Set weekly limit: ccswitch --set-limit <tokens>{NC}')
" 2>/dev/null || echo "Could not parse usage data"
}

# Set weekly token limit for current account
cmd_set_limit() {
    local limit="${1:-}"
    if [[ -z "$limit" ]]; then
        echo "Usage: ccswitch --set-limit <weekly_token_limit>"
        echo "  Example: ccswitch --set-limit 6700000000  (6.7B tokens)"
        echo "  Example: ccswitch --set-limit 6700M       (6.7B tokens)"
        return 1
    fi

    # Parse human-readable formats (6700M, 6.7B, etc)
    limit=$(echo "$limit" | python3 -c "
import sys
s = sys.stdin.read().strip().upper()
multipliers = {'K': 1_000, 'M': 1_000_000, 'B': 1_000_000_000, 'G': 1_000_000_000}
for suffix, mult in multipliers.items():
    if s.endswith(suffix):
        print(int(float(s[:-1]) * mult))
        sys.exit(0)
print(int(float(s)))
" 2>/dev/null)

    local active_num
    active_num=$(python3 -c "
import json
seq = json.load(open('$SEQUENCE_FILE'))
print(seq.get('activeAccountId', 1))
" 2>/dev/null)

    python3 -c "
import json
seq = json.load(open('$SEQUENCE_FILE'))
active = str($active_num)
if active in seq.get('accounts', {}):
    seq['accounts'][active]['weeklyTokenLimit'] = $limit
    json.dump(seq, open('$SEQUENCE_FILE', 'w'), indent=2)
    email = seq['accounts'][active].get('email', 'unknown')
    print(f'Set weekly limit for {email} to {$limit:,} tokens ({$limit/1_000_000:.0f}M)')
else:
    print('Error: active account not found')
" 2>/dev/null
}

# Show real server-side usage for all accounts via OAuth API
cmd_usage_all() {
    local json_mode="false"
    if [[ "${1:-}" == "--json" ]]; then
        json_mode="true"
    fi

    local account_nums
    account_nums=$(jq -r '.sequence[]' "$SEQUENCE_FILE")
    local active_num
    active_num=$(jq -r '.activeAccountId' "$SEQUENCE_FILE")
    local platform
    platform=$(detect_platform)

    if [[ "$json_mode" == "false" ]]; then
        echo -e "\033[1mAll Accounts - Usage\033[0m"
        echo ""
    fi

    # Collect results: query OAuth usage API per account (no switching needed)
    local results=""
    for num in $account_nums; do
        local email token usage_json h5 d7 h5_reset d7_reset sub_type
        email=$(jq -r ".accounts[\"$num\"].email" "$SEQUENCE_FILE")

        if [[ "$json_mode" == "false" ]]; then
            echo -ne "  \033[0;90mQuerying #${num} ${email}...\033[0m "
        fi

        # Get OAuth token via credential backend (auto-refresh if expired)
        token=""
        sub_type=""
        local cred_json
        if [[ "$num" == "$active_num" ]]; then
            cred_json=$(read_credentials)
        else
            cred_json=$(read_account_credentials "$num" "$email")
        fi
        # Auto-refresh expired tokens before querying
        if [[ -n "$cred_json" ]] && _token_is_expired "$cred_json"; then
            if refresh_account_token "$num" 2>/dev/null; then
                # Re-read after refresh
                if [[ "$num" == "$active_num" ]]; then
                    cred_json=$(read_credentials)
                else
                    cred_json=$(read_account_credentials "$num" "$email")
                fi
            fi
        fi
        if [[ -n "$cred_json" ]]; then
            token=$(_cred_field "$cred_json" accessToken)
            sub_type=$(_cred_field "$cred_json" subscriptionType)
        fi

        if [[ -z "$token" ]]; then
            [[ "$json_mode" == "false" ]] && echo -e "\033[0;31m✗ no token\033[0m"
            results="${results}${num}|${email}|—|—|—|—|${sub_type}|error
"
            continue
        fi

        # Call OAuth usage API
        usage_json=$(curl -sf --max-time 10 "https://api.anthropic.com/api/oauth/usage" \
            -H "Authorization: Bearer $token" \
            -H "anthropic-beta: oauth-2025-04-20" 2>/dev/null || echo "")

        if [[ -z "$usage_json" ]] || echo "$usage_json" | python3 -c "import sys,json; d=json.loads(sys.stdin.read()); sys.exit(0 if 'five_hour' in d else 1)" 2>/dev/null; then
            if [[ -z "$usage_json" ]]; then
                [[ "$json_mode" == "false" ]] && echo -e "\033[0;31m✗ API error\033[0m"
                results="${results}${num}|${email}|—|—|—|—|${sub_type}|error
"
                continue
            fi
            local parsed
            parsed=$(echo "$usage_json" | python3 -c "
import sys, json
d = json.loads(sys.stdin.read())
h5 = d.get('five_hour', {})
d7 = d.get('seven_day', {})
print(f\"{h5.get('utilization', '—')}|{d7.get('utilization', '—')}|{h5.get('resets_at', '')}|{d7.get('resets_at', '')}\")
" 2>/dev/null)
            [[ "$json_mode" == "false" ]] && echo -e "\033[0;32m✓\033[0m"
            results="${results}${num}|${email}|${parsed}|${sub_type}|ok
"
        else
            [[ "$json_mode" == "false" ]] && echo -e "\033[0;31m✗ token expired\033[0m"
            results="${results}${num}|${email}|—|—|—|—|${sub_type}|expired
"
        fi
    done

    # JSON output mode
    if [[ "$json_mode" == "true" ]]; then
        python3 -c "
import json, sys
results = '''$results'''.strip().split('\n')
active_num = '$active_num'
accounts = []
for line in results:
    if not line.strip(): continue
    parts = line.split('|')
    if len(parts) < 8: continue
    num, email, h5_str, d7_str, h5_reset, d7_reset, sub_type, status = parts
    acct = {
        'id': num,
        'email': email,
        'active': num == active_num,
        'subscription': sub_type or None,
        'status': status,
    }
    if status == 'ok':
        try:
            acct['five_hour'] = {'utilization': float(h5_str), 'resets_at': h5_reset}
            acct['seven_day'] = {'utilization': float(d7_str), 'resets_at': d7_reset}
        except:
            pass
    accounts.append(acct)
print(json.dumps({'accounts': accounts}, indent=2))
" 2>/dev/null
        return
    fi

    echo ""

    # Render
    python3 -c "
import sys
from datetime import datetime, timezone

BOLD = '\033[1m'
DIM = '\033[0;90m'
CYAN = '\033[0;36m'
GREEN = '\033[0;32m'
YELLOW = '\033[0;33m'
RED = '\033[0;31m'
NC = '\033[0m'

def pct_color(pct):
    if pct >= 80: return RED
    if pct >= 50: return YELLOW
    return GREEN

def bar(pct, width=20):
    filled = int(min(100, pct) / 100 * width)
    return '█' * filled + '░' * (width - filled)

def time_remaining(reset_str):
    if not reset_str: return ''
    try:
        reset = datetime.fromisoformat(reset_str)
        now = datetime.now(timezone.utc)
        delta = reset - now
        if delta.total_seconds() <= 0: return 'resetting'
        hours = int(delta.total_seconds() // 3600)
        mins = int((delta.total_seconds() % 3600) // 60)
        if hours > 24:
            days = hours // 24
            hours = hours % 24
            return f'{days}d {hours}h'
        return f'{hours}h {mins}m'
    except:
        return ''

active_num = '$active_num'
results = '''$results'''.strip().split('\n')

for line in results:
    if not line.strip(): continue
    parts = line.split('|')
    if len(parts) < 8: continue
    num, email, h5_str, d7_str, h5_reset, d7_reset, sub_type, status = parts

    is_active = num == active_num
    marker = f'{CYAN}◉{NC}' if is_active else f'{DIM}○{NC}'
    sub_label = f' ({sub_type})' if sub_type else ''

    print(f'  {marker} {BOLD}#{num}{NC} {email}{DIM}{sub_label}{NC}')

    if status == 'error':
        print(f'    {RED}Could not fetch usage{NC}')
    elif status == 'expired':
        print(f'    {YELLOW}Token expired - switch to this account to refresh{NC}')
    else:
        try:
            h5 = float(h5_str)
            d7 = float(d7_str)
            h5_remain = time_remaining(h5_reset)
            d7_remain = time_remaining(d7_reset)

            h5c = pct_color(h5)
            d7c = pct_color(d7)

            print(f'    5h:  {h5c}{bar(h5)} {h5:.0f}%{NC}  {DIM}resets in {h5_remain}{NC}')
            print(f'    7d:  {d7c}{bar(d7)} {d7:.0f}%{NC}  {DIM}resets in {d7_remain}{NC}')
        except:
            print(f'    {DIM}No usage data{NC}')

    print()
" 2>/dev/null || echo "Could not render usage data"
}

# Output eval-able exports to use a specific account in this shell only
# Sets CLAUDE_CONFIG_DIR to an isolated per-account directory with symlinks to shared config
#
# Usage:
#   eval "$(ccswitch --env 2)"                          # Use account 2 from managed accounts
#   eval "$(ccswitch --env --unset)"                    # Revert to global
#   eval "$(ccswitch --env --creds-file /path/to/creds.json)"                  # Use a credentials file
#   eval "$(ccswitch --env --creds-file /mnt/secrets/creds.json --config-dir /tmp/claude-ci)"  # Custom config dir
cmd_env() {
    local target="" creds_file="" custom_config_dir=""

    # Parse args
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --unset)
                echo "unset CLAUDE_CONFIG_DIR"
                echo "echo '[ccswitch] Reverted to global account'" >&2
                return 0
                ;;
            --creds-file)
                shift
                creds_file="${1:-}"
                ;;
            --config-dir)
                shift
                custom_config_dir="${1:-}"
                ;;
            *)
                target="$1"
                ;;
        esac
        shift
    done

    # ── Mode 1: credentials file (no managed account needed) ──
    if [[ -n "$creds_file" ]]; then
        if [[ ! -f "$creds_file" ]]; then
            echo "echo 'Error: Credentials file not found: $creds_file'" >&2
            return 1
        fi

        local config_dir="${custom_config_dir:-$HOME/.claude-env-file}"
        local shared_dir="$HOME/.claude"

        mkdir -p "$config_dir"

        # Symlink shared resources
        for item in settings.json CLAUDE.md mcp_servers.json hooks skills agents plugins commands scripts; do
            if [[ -e "$shared_dir/$item" ]] && [[ ! -e "$config_dir/$item" ]]; then
                ln -sf "$shared_dir/$item" "$config_dir/$item"
            fi
        done
        mkdir -p "$config_dir/projects"

        # Symlink or copy the credentials file
        if [[ "$creds_file" == /* ]]; then
            # Absolute path - symlink to it (supports mounted secrets)
            ln -sf "$creds_file" "$config_dir/.credentials.json"
        else
            # Relative path - resolve and symlink
            ln -sf "$(cd "$(dirname "$creds_file")" && pwd)/$(basename "$creds_file")" "$config_dir/.credentials.json"
        fi

        # Try to extract identity from the creds file
        python3 -c "
import json, sys
try:
    d = json.load(open('$creds_file'))
    oauth = d.get('claudeAiOauth', {})
    # If we can't get identity from creds, that's ok - Claude will figure it out
    out = {'hasCompletedOnboarding': True}
    json.dump(out, open('$config_dir/.claude.json', 'w'), indent=2)
except: pass
" 2>/dev/null

        echo "export CLAUDE_CONFIG_DIR=\"$config_dir\""
        echo "echo '[ccswitch] Shell bound to credentials file: $creds_file (CLAUDE_CONFIG_DIR=$config_dir)'" >&2
        return 0
    fi

    # ── Mode 2: managed account by hash/email/index ──
    if [[ -z "$target" ]]; then
        echo "echo 'Usage: eval \"\$(ccswitch --env <hash|email|index>)\"'" >&2
        echo "echo '        eval \"\$(ccswitch --env --creds-file /path/to/creds.json)\"'" >&2
        echo "echo '        eval \"\$(ccswitch --env --creds-file /path --config-dir /dir)\"'" >&2
        echo "echo 'Unset:  eval \"\$(ccswitch --env --unset)\"'" >&2
        return 1
    fi

    # Resolve to account hash ID
    local account_id
    account_id=$(resolve_account_identifier "$target")
    if [[ -z "$account_id" ]]; then
        echo "echo 'Error: Account not found: $target'" >&2
        return 1
    fi
    local account_email
    account_email=$(get_account_email "$account_id")

    local config_dir="${custom_config_dir:-$HOME/.claude-env-${account_id}}"
    local shared_dir="$HOME/.claude"

    # Get OAuth credentials for this account via backend
    local cred_json=""
    local active_id
    active_id=$(jq -r '.activeAccountId' "$SEQUENCE_FILE")
    if [[ "$account_id" == "$active_id" ]]; then
        cred_json=$(read_credentials)
    else
        cred_json=$(read_account_credentials "$account_id" "$account_email")
    fi

    if [[ -z "$cred_json" ]]; then
        echo "echo 'Error: No credentials found for ${account_id} (${account_email})'" >&2
        return 1
    fi

    # Create isolated config dir with symlinks to shared config
    mkdir -p "$config_dir"
    for item in settings.json CLAUDE.md mcp_servers.json hooks skills agents plugins commands scripts; do
        if [[ -e "$shared_dir/$item" ]] && [[ ! -e "$config_dir/$item" ]]; then
            ln -sf "$shared_dir/$item" "$config_dir/$item"
        fi
    done
    mkdir -p "$config_dir/projects"

    # Write credentials file
    echo "$cred_json" > "$config_dir/.credentials.json"
    chmod 600 "$config_dir/.credentials.json"

    # Write identity from config backup
    local config_backup="$BACKUP_DIR/configs/.claude-config-${account_id}-${account_email}.json"
    if [[ -f "$config_backup" ]]; then
        python3 -c "
import json
backup = json.load(open('$config_backup'))
oauth = backup.get('oauthAccount', {})
out = {'oauthAccount': oauth, 'hasCompletedOnboarding': True}
json.dump(out, open('$config_dir/.claude.json', 'w'), indent=2)
" 2>/dev/null
    fi

    echo "export CLAUDE_CONFIG_DIR=\"$config_dir\""
    echo "echo '[ccswitch] Shell bound to ${account_id} ${account_email} (CLAUDE_CONFIG_DIR=$config_dir)'" >&2
}

# Show help
show_usage() {
    echo "Multi-Account Switcher for Claude Code"
    echo "Usage: $0 [COMMAND]"
    echo ""
    echo "Account Management:"
    echo "  --add-account                    Add current account to managed accounts"
    echo "  --remove-account <num|email>     Remove account by number or email"
    echo "  --current                        Show current active account"
    echo "  --list                           List all managed accounts"
    echo "  --switch                         Rotate to next account in sequence"
    echo "  --switch-to <num|email>          Switch to specific account number or email"
    echo "  --save                           Save active credentials to backup (after login/re-auth)"
    echo "  --env <num|email>               Output exports for per-shell account (use with eval)"
    echo "  --env --creds-file <path>       Use a credentials file (mountable secret)"
    echo "  --env --creds-file <p> --config-dir <d>  Custom config dir + creds file"
    echo "  --env --unset                   Revert shell to global account"
    echo "  --refresh-all                   Refresh expired OAuth tokens for all accounts"
    echo "  --login                          Interactive login for accounts with expired credentials"
    echo "  --login --only <hash|email>      Log in to a specific account"
    echo "  --login --force                  Re-login to all accounts (even if valid)"
    echo ""
    echo "Usage Monitoring:"
    echo "  --usage                          Show 5h block and weekly usage limits"
    echo "  --usage-all [--json]             Show usage for all accounts (optional JSON output)"
    echo "  --set-limit <tokens>             Set weekly token limit for current account (e.g. 6700M)"
    echo ""
    echo "API Configuration:"
    echo "  --use-zai                        Switch to z.ai API (ztaylor@stigen.ai)"
    echo "  --use-anthropic                  Revert to default Anthropic API"
    echo "  --api-status                     Show current API configuration"
    echo ""
    echo "Configuration:"
    echo "  --config                         Show effective configuration"
    echo "  --init-config                    Create a TOML config template at ~/.config/ccswitch/config.toml"
    echo "  --backend                        Show current credential backend (alias for part of --config)"
    echo ""
    echo "Config file: ${CCSWITCH_CONFIG_FILE}"
    echo "Env overrides:"
    echo "  CCSWITCH_BACKEND=<backend>       Set backend: auto, keychain, file, 1password, vault"
    echo "  CCSWITCH_OP_VAULT=<vault>        1Password vault (default: Private)"
    echo "  CCSWITCH_OP_ACCOUNT=<shorthand>  1Password account shorthand (for --account)"
    echo "  CCSWITCH_VAULT_ADDR=<url>        Vault/OpenBao address"
    echo "  CCSWITCH_VAULT_PATH=<path>       Vault KV path (default: secret/data/ccswitch)"
    echo ""
    echo "1Password Sync:"
    echo "  --push                           Push all local credentials + metadata to 1Password"
    echo "  --pull                           Pull all credentials + metadata from 1Password"
    echo "  --sync [--quiet]                 Bi-directional sync (newest-expiry wins)"
    echo "  --daemon                         Run sync loop continuously (CCSWITCH_SYNC_INTERVAL, default 300s)"
    echo "  --setup-op-connect               Configure 1Password Connect (URL + token) — no biometric prompts"
    echo ""
    echo "  --help                           Show this help message"
    echo ""
    echo "Examples:"
    echo "  $0 --add-account"
    echo "  $0 --current"
    echo "  $0 --list"
    echo "  $0 --switch"
    echo "  $0 --switch-to 2"
    echo "  $0 --switch-to user@example.com"
    echo "  $0 --remove-account user@example.com"
    echo "  $0 --use-zai                     # Use z.ai API with 1Password auth"
    echo "  $0 --use-anthropic               # Revert to default API"
    echo "  eval \"\$($0 --env 2)\"             # Bind this shell to account 2"
    echo "  eval \"\$($0 --env --creds-file /mnt/secrets/claude.json)\"  # Mount a secret"
    echo "  eval \"\$($0 --env --creds-file /s/creds.json --config-dir /tmp/ci)\"  # CI/container"
    echo "  eval \"\$($0 --env --unset)\"       # Revert to global account"
}

# Main script logic
main() {
    # Basic checks - allow root execution in containers
    if [[ $EUID -eq 0 ]] && ! is_running_in_container; then
        echo "Error: Do not run this script as root (unless running in a container)"
        exit 1
    fi
    
    check_bash_version
    check_dependencies

    # Run migration on any command that needs state (skips if v2 or no file)
    if [[ -f "$SEQUENCE_FILE" ]]; then
        migrate_v1_to_v2
    fi

    case "${1:-}" in
        --add-account)
            cmd_add_account
            ;;
        --remove-account)
            shift
            cmd_remove_account "$@"
            ;;
        --current)
            cmd_current
            ;;
        --list)
            cmd_list
            ;;
        --switch)
            cmd_switch
            ;;
        --switch-to)
            shift
            cmd_switch_to "$@"
            ;;
        --use-zai)
            cmd_use_zai
            ;;
        --use-anthropic)
            cmd_use_anthropic
            ;;
        --api-status)
            cmd_api_status
            ;;
        --save)
            cmd_save
            ;;
        --usage)
            cmd_usage
            ;;
        --set-limit)
            shift
            cmd_set_limit "$@"
            ;;
        --usage-all)
            shift
            cmd_usage_all "$@"
            ;;
        --env)
            shift
            cmd_env "$@"
            ;;
        --refresh-all)
            cmd_refresh_all
            ;;
        --login)
            shift
            cmd_login "$@"
            ;;
        --config)
            cmd_config
            ;;
        --init-config)
            cmd_init_config
            ;;
        --setup-op-connect)
            cmd_setup_op_connect
            ;;
        --push)
            cmd_push
            ;;
        --pull)
            cmd_pull
            ;;
        --sync)
            shift
            cmd_sync "$@"
            ;;
        --daemon)
            cmd_daemon
            ;;
        --backend)
            local b
            b=$(_resolve_backend)
            echo "Backend: $b (CCSWITCH_BACKEND=${CCSWITCH_BACKEND})"
            case "$b" in
                1password) echo "  Vault: $CCSWITCH_OP_VAULT"; echo "  Prefix: $CCSWITCH_OP_ITEM_PREFIX" ;;
                vault) echo "  Addr: ${CCSWITCH_VAULT_ADDR:-<not set>}"; echo "  Path: $CCSWITCH_VAULT_PATH" ;;
            esac
            ;;
        --help)
            show_usage
            ;;
        "")
            show_usage
            ;;
        *)
            echo "Error: Unknown command '$1'"
            show_usage
            exit 1
            ;;
    esac
}

# Check if script is being sourced or executed
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    main "$@"
fi