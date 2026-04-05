#!/usr/bin/env bash

# Multi-Account Switcher for Claude Code
# Simple tool to manage and switch between multiple Claude Code accounts

set -euo pipefail

# Configuration
readonly BACKUP_DIR="$HOME/.claude-switch-backup"
readonly SEQUENCE_FILE="$BACKUP_DIR/sequence.json"

# Credential backend: auto, keychain, file, 1password, vault
# Set via CCSWITCH_BACKEND env var or --backend flag
# "auto" = keychain on macOS, file on Linux
CCSWITCH_BACKEND="${CCSWITCH_BACKEND:-auto}"

# 1Password settings
CCSWITCH_OP_VAULT="${CCSWITCH_OP_VAULT:-Private}"
CCSWITCH_OP_ITEM_PREFIX="${CCSWITCH_OP_ITEM_PREFIX:-Claude Code Account}"

# HashiCorp Vault / OpenBao settings
CCSWITCH_VAULT_ADDR="${CCSWITCH_VAULT_ADDR:-${VAULT_ADDR:-}}"
CCSWITCH_VAULT_PATH="${CCSWITCH_VAULT_PATH:-secret/data/ccswitch}"
CCSWITCH_VAULT_TOKEN="${CCSWITCH_VAULT_TOKEN:-${VAULT_TOKEN:-}}"

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

# Account identifier resolution function
resolve_account_identifier() {
    local identifier="$1"
    if [[ "$identifier" =~ ^[0-9]+$ ]]; then
        echo "$identifier"  # It's a number
    else
        # Look up account number by email
        local account_num
        account_num=$(jq -r --arg email "$identifier" '.accounts | to_entries[] | select(.value.email == $email) | .key' "$SEQUENCE_FILE" 2>/dev/null)
        if [[ -n "$account_num" && "$account_num" != "null" ]]; then
            echo "$account_num"
        else
            echo ""
        fi
    fi
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
    security add-generic-password -U -s "$service" -a "$USER" -w "$credentials" 2>/dev/null
}

_keychain_delete() {
    local service="$1"
    security delete-generic-password -s "$service" 2>/dev/null || true
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

_op_read() {
    local item_name="$1"
    if ! command -v op &>/dev/null; then
        echo ""
        return
    fi
    op item get "$item_name" --vault "$CCSWITCH_OP_VAULT" --fields label=credentials --format json 2>/dev/null | jq -r '.value // empty' 2>/dev/null || echo ""
}

_op_write() {
    local item_name="$1" credentials="$2"
    if ! command -v op &>/dev/null; then
        echo "Error: 1password-cli (op) not installed" >&2
        return 1
    fi
    # Check if item exists
    if op item get "$item_name" --vault "$CCSWITCH_OP_VAULT" &>/dev/null 2>&1; then
        # Update existing item
        op item edit "$item_name" --vault "$CCSWITCH_OP_VAULT" "credentials=$credentials" &>/dev/null
    else
        # Create new item
        op item create --category "Secure Note" --title "$item_name" --vault "$CCSWITCH_OP_VAULT" "credentials=$credentials" &>/dev/null
    fi
}

_op_delete() {
    local item_name="$1"
    if command -v op &>/dev/null; then
        op item delete "$item_name" --vault "$CCSWITCH_OP_VAULT" &>/dev/null 2>&1 || true
    fi
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
  "activeAccountNumber": null,
  "lastUpdated": "'$(date -u +%Y-%m-%dT%H:%M:%SZ)'",
  "sequence": [],
  "accounts": {}
}'
        write_json "$SEQUENCE_FILE" "$init_content"
    fi
}

# Get next account number
get_next_account_number() {
    if [[ ! -f "$SEQUENCE_FILE" ]]; then
        echo "1"
        return
    fi
    
    local max_num
    max_num=$(jq -r '.accounts | keys | map(tonumber) | max // 0' "$SEQUENCE_FILE")
    echo $((max_num + 1))
}

# Check if account exists by email
account_exists() {
    local email="$1"
    if [[ ! -f "$SEQUENCE_FILE" ]]; then
        return 1
    fi
    
    jq -e --arg email "$email" '.accounts[] | select(.email == $email)' "$SEQUENCE_FILE" >/dev/null 2>&1
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
    
    local account_num
    account_num=$(get_next_account_number)
    
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
    write_account_credentials "$account_num" "$current_email" "$current_creds"
    write_account_config "$account_num" "$current_email" "$current_config"
    
    # Update sequence.json
    local updated_sequence
    updated_sequence=$(jq --arg num "$account_num" --arg email "$current_email" --arg uuid "$account_uuid" --arg now "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '
        .accounts[$num] = {
            email: $email,
            uuid: $uuid,
            added: $now
        } |
        .sequence += [$num | tonumber] |
        .activeAccountNumber = ($num | tonumber) |
        .lastUpdated = $now
    ' "$SEQUENCE_FILE")
    
    write_json "$SEQUENCE_FILE" "$updated_sequence"
    
    echo "Added Account $account_num: $current_email"
}

# Remove account
cmd_remove_account() {
    if [[ $# -eq 0 ]]; then
        echo "Usage: $0 --remove-account <account_number|email>"
        exit 1
    fi
    
    local identifier="$1"
    local account_num
    
    if [[ ! -f "$SEQUENCE_FILE" ]]; then
        echo "Error: No accounts are managed yet"
        exit 1
    fi
    
    # Handle email vs numeric identifier
    if [[ "$identifier" =~ ^[0-9]+$ ]]; then
        account_num="$identifier"
    else
        # Validate email format
        if ! validate_email "$identifier"; then
            echo "Error: Invalid email format: $identifier"
            exit 1
        fi
        
        # Resolve email to account number
        account_num=$(resolve_account_identifier "$identifier")
        if [[ -z "$account_num" ]]; then
            echo "Error: No account found with email: $identifier"
            exit 1
        fi
    fi
    
    local account_info
    account_info=$(jq -r --arg num "$account_num" '.accounts[$num] // empty' "$SEQUENCE_FILE")
    
    if [[ -z "$account_info" ]]; then
        echo "Error: Account-$account_num does not exist"
        exit 1
    fi
    
    local email
    email=$(echo "$account_info" | jq -r '.email')
    
    local active_account
    active_account=$(jq -r '.activeAccountNumber' "$SEQUENCE_FILE")
    
    if [[ "$active_account" == "$account_num" ]]; then
        echo "Warning: Account-$account_num ($email) is currently active"
    fi
    
    echo -n "Are you sure you want to permanently remove Account-$account_num ($email)? [y/N] "
    read -r confirm
    
    if [[ "$confirm" != "y" && "$confirm" != "Y" ]]; then
        echo "Cancelled"
        exit 0
    fi
    
    # Remove backup credentials via backend
    delete_account_credentials "$account_num" "$email"

    # Legacy cleanup (in case of backend migration)
    local platform
    platform=$(detect_platform)
    case "$platform" in
        macos)
            security delete-generic-password -s "Claude Code-Account-${account_num}-${email}" 2>/dev/null || true
            ;;
        linux|wsl)
            rm -f "$BACKUP_DIR/credentials/.claude-credentials-${account_num}-${email}.json"
            ;;
    esac
    rm -f "$BACKUP_DIR/configs/.claude-config-${account_num}-${email}.json"
    
    # Update sequence.json
    local updated_sequence
    updated_sequence=$(jq --arg num "$account_num" --arg now "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '
        del(.accounts[$num]) |
        .sequence = (.sequence | map(select(. != ($num | tonumber)))) |
        .lastUpdated = $now
    ' "$SEQUENCE_FILE")
    
    write_json "$SEQUENCE_FILE" "$updated_sequence"
    
    echo "Account-$account_num ($email) has been removed"
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

    # Find which account number corresponds to the current email
    local active_account_num=""
    if [[ "$current_email" != "none" ]]; then
        active_account_num=$(jq -r --arg email "$current_email" '.accounts | to_entries[] | select(.value.email == $email) | .key' "$SEQUENCE_FILE" 2>/dev/null)
    fi

    echo "Accounts:"

    # Iterate through accounts and display with org names
    local sequence_nums
    sequence_nums=$(jq -r '.sequence[]' "$SEQUENCE_FILE")

    while IFS= read -r num; do
        local email org_name
        email=$(jq -r --arg num "$num" '.accounts[$num].email' "$SEQUENCE_FILE")

        if [[ "$num" == "$active_account_num" ]]; then
            # For active account, get org from current config
            org_name="$current_org"
            echo "  $num: $email [$org_name] (active)"
        else
            # For other accounts, get org from backup
            org_name=$(get_account_org_name "$num" "$email")
            echo "  $num: $email [$org_name]"
        fi
    done <<< "$sequence_nums"
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
        local account_num
        account_num=$(jq -r '.activeAccountNumber' "$SEQUENCE_FILE")
        echo "It has been automatically added as Account-$account_num."
        echo "Please run './ccswitch.sh --switch' again to switch to the next account."
        exit 0
    fi
    
    # wait_for_claude_close
    
    local active_account sequence
    active_account=$(jq -r '.activeAccountNumber' "$SEQUENCE_FILE")
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

# Switch to specific account
cmd_switch_to() {
    if [[ $# -eq 0 ]]; then
        echo "Usage: $0 --switch-to <account_number|email>"
        exit 1
    fi
    
    local identifier="$1"
    local target_account
    
    if [[ ! -f "$SEQUENCE_FILE" ]]; then
        echo "Error: No accounts are managed yet"
        exit 1
    fi
    
    # Handle email vs numeric identifier
    if [[ "$identifier" =~ ^[0-9]+$ ]]; then
        target_account="$identifier"
    else
        # Validate email format
        if ! validate_email "$identifier"; then
            echo "Error: Invalid email format: $identifier"
            exit 1
        fi
        
        # Resolve email to account number
        target_account=$(resolve_account_identifier "$identifier")
        if [[ -z "$target_account" ]]; then
            echo "Error: No account found with email: $identifier"
            exit 1
        fi
    fi
    
    local account_info
    account_info=$(jq -r --arg num "$target_account" '.accounts[$num] // empty' "$SEQUENCE_FILE")
    
    if [[ -z "$account_info" ]]; then
        echo "Error: Account-$target_account does not exist"
        exit 1
    fi
    
    # wait_for_claude_close
    perform_switch "$target_account"
}

# Perform the actual account switch
perform_switch() {
    local target_account="$1"
    
    # Get current and target account info
    local current_account target_email current_email
    current_account=$(jq -r '.activeAccountNumber' "$SEQUENCE_FILE")
    target_email=$(jq -r --arg num "$target_account" '.accounts[$num].email' "$SEQUENCE_FILE")
    current_email=$(get_current_account)
    
    # Step 1: Backup current account
    local current_creds current_config
    current_creds=$(read_credentials)
    current_config=$(cat "$(get_claude_config_path)")
    
    write_account_credentials "$current_account" "$current_email" "$current_creds"
    write_account_config "$current_account" "$current_email" "$current_config"
    
    # Step 2: Retrieve target account
    local target_creds target_config
    target_creds=$(read_account_credentials "$target_account" "$target_email")
    target_config=$(read_account_config "$target_account" "$target_email")
    
    if [[ -z "$target_creds" || -z "$target_config" ]]; then
        echo "Error: Missing backup data for Account-$target_account"
        exit 1
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
    
    # Merge with current config and validate
    local merged_config
    merged_config=$(jq --argjson oauth "$oauth_section" '.oauthAccount = $oauth' "$(get_claude_config_path)" 2>/dev/null)
    if [[ $? -ne 0 ]]; then
        echo "Error: Failed to merge config"
        exit 1
    fi
    
    # Use existing safe write_json function
    write_json "$(get_claude_config_path)" "$merged_config"
    
    # Step 4: Update state (track activation times per account for usage attribution)
    local updated_sequence
    updated_sequence=$(jq --arg num "$target_account" --arg prev "$current_account" --arg now "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '
        .activeAccountNumber = ($num | tonumber) |
        .lastUpdated = $now |
        .accounts[$num].activeSince = $now |
        .accounts[$prev].lastDeactivated = $now |
        .switchLog = ((.switchLog // []) + [{"from": ($prev | tonumber), "to": ($num | tonumber), "at": $now}]) |
        .switchLog = (.switchLog | if length > 100 then .[-100:] else . end)
    ' "$SEQUENCE_FILE")
    
    write_json "$SEQUENCE_FILE" "$updated_sequence"
    
    echo "Switched to Account-$target_account ($target_email)"
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
    active = str(seq.get('activeAccountNumber', 1))
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
print(seq.get('activeAccountNumber', 1))
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
    account_nums=$(jq -r '.accounts | keys[]' "$SEQUENCE_FILE" | sort -n)
    local active_num
    active_num=$(jq -r '.activeAccountNumber' "$SEQUENCE_FILE")
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

        # Get OAuth token via credential backend
        token=""
        sub_type=""
        local cred_json
        if [[ "$num" == "$active_num" ]]; then
            cred_json=$(read_credentials)
        else
            cred_json=$(read_account_credentials "$num" "$email")
        fi
        if [[ -n "$cred_json" ]]; then
            token=$(echo "$cred_json" | python3 -c "import sys,json; print(json.loads(sys.stdin.read()).get('claudeAiOauth',{}).get('accessToken',''))" 2>/dev/null)
            sub_type=$(echo "$cred_json" | python3 -c "import sys,json; print(json.loads(sys.stdin.read()).get('claudeAiOauth',{}).get('subscriptionType',''))" 2>/dev/null)
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
        'account': int(num),
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
cmd_env() {
    local target="${1:-}"
    if [[ -z "$target" ]]; then
        echo "echo 'Usage: eval \"\$(ccswitch --env <account_number|email>)\"'" >&2
        echo "echo 'Unset: eval \"\$(ccswitch --env --unset)\"'" >&2
        return 1
    fi

    # Handle --unset
    if [[ "$target" == "--unset" ]]; then
        echo "unset CLAUDE_CONFIG_DIR"
        echo "echo '[ccswitch] Reverted to global account'" >&2
        return 0
    fi

    # Resolve account number
    local account_num=""
    local account_email=""

    if [[ "$target" =~ ^[0-9]+$ ]]; then
        account_num="$target"
        account_email=$(jq -r ".accounts[\"$account_num\"].email // empty" "$SEQUENCE_FILE")
    else
        # Search by email
        account_num=$(jq -r ".accounts | to_entries[] | select(.value.email == \"$target\") | .key" "$SEQUENCE_FILE")
        account_email="$target"
    fi

    if [[ -z "$account_num" ]] || [[ -z "$account_email" ]]; then
        echo "echo 'Error: Account not found: $target'" >&2
        return 1
    fi

    local config_dir="$HOME/.claude-env-${account_num}"
    local shared_dir="$HOME/.claude"
    local platform
    platform=$(detect_platform)

    # Get OAuth credentials for this account via backend
    local cred_json=""
    local active_num
    active_num=$(jq -r '.activeAccountNumber' "$SEQUENCE_FILE")
    if [[ "$account_num" == "$active_num" ]]; then
        cred_json=$(read_credentials)
    else
        cred_json=$(read_account_credentials "$account_num" "$account_email")
    fi

    if [[ -z "$cred_json" ]]; then
        echo "echo 'Error: No credentials found for account #${account_num} (${account_email})'" >&2
        return 1
    fi

    # Create isolated config dir with symlinks to shared config
    mkdir -p "$config_dir"

    # Symlink shared resources (read-only is fine)
    for item in settings.json CLAUDE.md mcp_servers.json hooks skills agents plugins commands scripts; do
        if [[ -e "$shared_dir/$item" ]] && [[ ! -e "$config_dir/$item" ]]; then
            ln -sf "$shared_dir/$item" "$config_dir/$item"
        fi
    done

    # Create projects dir (must be writable, separate per account)
    mkdir -p "$config_dir/projects"

    # Write credentials file for this account (CLAUDE_CONFIG_DIR uses file-based creds)
    echo "$cred_json" > "$config_dir/.credentials.json"
    chmod 600 "$config_dir/.credentials.json"

    # Write the oauthAccount identity so Claude knows which account this is
    local oauth_account
    oauth_account=$(jq -r ".accounts[\"$account_num\"]" "$SEQUENCE_FILE")
    # Get the full config backup if available
    local config_backup="$BACKUP_DIR/configs/.claude-config-${account_num}-${account_email}.json"
    if [[ -f "$config_backup" ]]; then
        # Extract oauthAccount from backup and write a minimal .claude.json
        python3 -c "
import json
backup = json.load(open('$config_backup'))
oauth = backup.get('oauthAccount', {})
# Minimal .claude.json with just the account identity
out = {'oauthAccount': oauth, 'hasCompletedOnboarding': True}
json.dump(out, open('$config_dir/.claude.json', 'w'), indent=2)
" 2>/dev/null
    fi

    # Output the export statement
    echo "export CLAUDE_CONFIG_DIR=\"$config_dir\""
    echo "echo '[ccswitch] Shell bound to #${account_num} ${account_email} (CLAUDE_CONFIG_DIR=$config_dir)'" >&2
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
    echo "  --env <num|email>               Output exports for per-shell account (use with eval)"
    echo "  --env --unset                   Revert shell to global account"
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
    echo "Credential Backend:"
    echo "  --backend                        Show current credential backend"
    echo "  CCSWITCH_BACKEND=<backend>       Set backend: auto, keychain, file, 1password, vault"
    echo "  CCSWITCH_OP_VAULT=<vault>        1Password vault (default: Private)"
    echo "  CCSWITCH_VAULT_ADDR=<url>        Vault/OpenBao address"
    echo "  CCSWITCH_VAULT_PATH=<path>       Vault KV path (default: secret/data/ccswitch)"
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