#!/usr/bin/env bash
# End-to-end checks for sync, switch locking, and Connect timeouts. Uses a
# throwaway Vault dev server as the remote store and toxiproxy to inject
# latency / a hung server. Nothing real is touched: no keychain, no network.
#
# Runs ccswitch-new (the build under test; failures set the exit code) and,
# when present, ccswitch-old (an older ref, `make e2e E2E_OLD=<ref>`) whose
# results are informational — it is expected to fail what was since fixed.
set -uo pipefail

VAULT=http://vault:8200
TOXI=http://toxiproxy:8474
ALICE=ff8d9819 BOB=5ff860bf
FAILS=0

until curl -sf "$VAULT/v1/sys/health" >/dev/null; do sleep 0.2; done
until curl -sf "$TOXI/version" >/dev/null; do sleep 0.2; done

ms() { date +%s%3N; }
pass() { echo "  PASS  $*"; }
fail() { echo "  FAIL  $*"; FAILS=$((FAILS + 1)); }

cred() { # token expiresAt-ms
  printf '{"claudeAiOauth":{"accessToken":"%s","refreshToken":"r-%s","expiresAt":%s}}' "$1" "$1" "$2"
}

# fresh_home NAME: isolated HOME with alice live + active, bob idle, and a
# fresh KV mount so runs don't see each other's data.
fresh_home() {
  export HOME=/tmp/home-$1
  rm -rf "$HOME"
  local c="$HOME/.claude-switch-backup/credentials"
  mkdir -p "$c" "$HOME/.claude"
  cat >"$HOME/.claude-switch-backup/sequence.json" <<JSON
{"version":2,"activeAccountId":"$ALICE","sequence":["$ALICE","$BOB"],
 "accounts":{
  "$ALICE":{"email":"alice@example.com","oauthAccount":{"emailAddress":"alice@example.com"}},
  "$BOB":{"email":"bob@example.com","oauthAccount":{"emailAddress":"bob@example.com"}}}}
JSON
  echo '{"oauthAccount":{"emailAddress":"alice@example.com"}}' >"$HOME/.claude.json"
  T1=$(( $(date +%s) * 1000 + 7200000 ))
  T2=$(( T1 + 3600000 ))
  cred alice-tok "$T1" >"$c/.Claude Code-credentials.json"
  cred alice-tok "$T1" >"$c/.Claude Code-Account-$ALICE-alice@example.com.json"
  cred bob-tok "$T1" >"$c/.Claude Code-Account-$BOB-bob@example.com.json"

  export CCSWITCH_BACKEND=vault CCSWITCH_LOCAL_BACKEND=file
  export CCSWITCH_CONFIG_FILE="$HOME/.config/ccswitch/config.toml"
  export CCSWITCH_VAULT_TOKEN=root CCSWITCH_VAULT_PATH="kv-$1" CCSWITCH_VAULT_ADDR=$VAULT
  curl -sf -H 'X-Vault-Token: root' -X POST -d '{"type":"kv","options":{"version":"2"}}' \
    "$VAULT/v1/sys/mounts/kv-$1" >/dev/null || echo "  (mount kv-$1 failed)"
}

vault_cred() { # key → stored credentials JSON
  local key; key=$(jq -rn --arg k "$1" '$k|@uri')
  curl -sf -H 'X-Vault-Token: root' "$VAULT/v1/$CCSWITCH_VAULT_PATH/data/$key" | jq -r '.data.data.credentials'
}

toxi_proxy() { # name listen upstream
  curl -sf -X DELETE "$TOXI/proxies/$1" >/dev/null
  curl -sf -X POST -d "{\"name\":\"$1\",\"listen\":\"$2\",\"upstream\":\"$3\"}" "$TOXI/proxies" >/dev/null
}

# 1. Daemon must not file the new account's live token under the old one
#    after a switch (stale in-memory sequence bug).
test_daemon() {
  local bin=$1 tag=$2
  fresh_home "daemon-$tag"
  "$bin" push >/dev/null 2>&1 || { fail "daemon: seed push failed"; return; }
  CCSWITCH_SYNC_INTERVAL=1 "$bin" daemon >"$HOME/daemon.log" 2>&1 &
  local dpid=$!
  sleep 2
  "$bin" switch-to bob@example.com >/dev/null 2>&1 || fail "daemon: switch-to failed"
  # Claude Code refreshes bob's token after the switch → live slot is newest.
  cred bob-refreshed "$T2" >"$HOME/.claude-switch-backup/credentials/.Claude Code-credentials.json"
  sleep 4
  kill $dpid 2>/dev/null; wait $dpid 2>/dev/null
  local a b
  a=$(vault_cred "Claude Code-Account-$ALICE-alice@example.com" | jq -r .claudeAiOauth.accessToken)
  b=$(vault_cred "Claude Code-Account-$BOB-bob@example.com" | jq -r .claudeAiOauth.accessToken)
  if [[ $a == alice-tok && $b == bob-refreshed ]]; then
    pass "daemon: remote alice=$a bob=$b"
  else
    fail "daemon: remote alice=$a (want alice-tok) bob=$b (want bob-refreshed)"
  fi
}

# 2. switch must release Claude Code's lock before the slow remote write.
test_lock() {
  local bin=$1 tag=$2
  fresh_home "lock-$tag"
  "$bin" push >/dev/null 2>&1 || { fail "lock: seed push failed"; return; }
  toxi_proxy slowvault 0.0.0.0:8201 vault:8200
  curl -sf -X POST -d '{"type":"latency","attributes":{"latency":2000}}' \
    "$TOXI/proxies/slowvault/toxics" >/dev/null
  local start first=0 last=0 now
  start=$(ms)
  CCSWITCH_VAULT_ADDR=http://toxiproxy:8201 "$bin" switch-to bob@example.com >"$HOME/switch.log" 2>&1 &
  local spid=$!
  while kill -0 $spid 2>/dev/null; do
    now=$(ms)
    if [[ -d $HOME/.claude.lock ]]; then (( first == 0 )) && first=$now; last=$now; fi
  done
  wait $spid; local rc=$? total=$(( $(ms) - start ))
  local held=$(( last - first ))
  local live; live=$(jq -r .claudeAiOauth.accessToken "$HOME/.claude-switch-backup/credentials/.Claude Code-credentials.json")
  local snap; snap=$(vault_cred "Claude Code-Account-$ALICE-alice@example.com" | jq -r .claudeAiOauth.accessToken)
  local local_snap; local_snap=$(jq -r .claudeAiOauth.accessToken "$HOME/.claude-switch-backup/credentials/.Claude Code-Account-$ALICE-alice@example.com.json")
  echo "        switch rc=$rc total=${total}ms lock held≈${held}ms live=$live remote-alice=$snap local-alice=$local_snap"
  if (( rc == 0 && held < 1000 )) && [[ $live == bob-tok && $snap == alice-tok && $local_snap == alice-tok ]]; then
    pass "lock: held ${held}ms of ${total}ms"
  else
    fail "lock: held ${held}ms of ${total}ms (want < 1000ms with correct slots)"
  fi
}

# 3. An unreachable (hung) Connect host must fail in seconds, not 30s.
test_connect() {
  local bin=$1 tag=$2
  fresh_home "connect-$tag"
  toxi_proxy blackhole 0.0.0.0:8443 vault:8200
  curl -sf -X POST -d '{"type":"timeout","stream":"upstream","attributes":{"timeout":0}}' \
    "$TOXI/proxies/blackhole/toxics" >/dev/null
  mkdir -p "$HOME/.config/ccswitch"
  echo fake >"$HOME/.config/ccswitch/connect-token"
  local start; start=$(ms)
  CCSWITCH_BACKEND=1password CCSWITCH_OP_CONNECT_HOST=https://toxiproxy:8443 CCSWITCH_OP_VAULT=x \
    timeout 60 "$bin" sync >"$HOME/sync.log" 2>&1
  local rc=$? el=$(( $(ms) - start ))
  if (( rc != 0 && el < 15000 )); then
    pass "connect: failed after ${el}ms"
  else
    fail "connect: rc=$rc after ${el}ms (want failure < 15000ms)"
  fi
  tail -1 "$HOME/sync.log" | cut -c1-160 | sed 's/^/        /'
}

for v in old new; do
  bin=/bin-ccswitch/ccswitch-$v
  [[ -x $bin ]] || continue
  echo "== ccswitch-$v =="
  FAILS=0
  test_daemon "$bin" "$v"
  test_lock "$bin" "$v"
  test_connect "$bin" "$v"
  echo "ccswitch-$v failures: $FAILS"
done
exit $(( FAILS > 0 ))
