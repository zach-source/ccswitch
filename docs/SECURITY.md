# Security model

ccswitch handles long-lived OAuth credentials. This doc captures the threat
model and the residual risks the shell implementation cannot fully eliminate.

## Threat model

ccswitch protects credentials from:

- **Other users on the same machine** — credentials live in the user's login
  Keychain (or 0600 files), inaccessible to other UIDs.
- **Cloud breach** — when the 1Password Connect backend is enabled, the local
  daemon talks to a Connect server through Cloudflare Access. The bearer
  token + CF Access service-token secrets are stored in the login Keychain.
- **Process accident** — config TOML never contains secrets; only references
  to keychain entries. A leaked config file does not expose credentials.

ccswitch does **not** defend against:

- A process running as the same UID as the user. Such a process can already
  call `security find-generic-password`, read `~/.claude/.credentials.json`,
  attach via ptrace, or read environment variables of any other process the
  user owns. Argv visibility (next section) is strictly weaker than this.

## Argv exposure (residual risk)

Two code paths briefly pass secrets through subprocess argv on macOS, where
`ps -ef` reveals full argv to same-UID observers. We mitigate where we can;
where we cannot, we accept the residual risk.

### `_op_connect_api` — Connect HTTP calls

**Closed.** Curl gets its bearer + CF Access headers via a config file fed
through process substitution (`-K <(...)`), and request bodies arrive on
stdin (`--data-binary @-`). Neither tokens nor credential JSON appear in
the curl process arg list. Verify with:

```bash
ps -ef | grep curl
# headers and body are absent; only the URL and HTTP method are visible
```

### `_keychain_write` / `_store_secret` — Keychain writes

**Open, by Apple design.** The macOS `security(1)` CLI accepts the password
only via `-w VALUE` (in argv). The interactive `bare -w` form requires a
double-prompt with retype confirmation that is unusable from non-interactive
scripts. There is no documented stdin variant.

The credential value therefore appears in the `security` subprocess's argv
during a single short-lived invocation (~50 ms). Same-UID `ps` polling
*could* observe it; cross-UID observers cannot.

To eliminate this entirely, ccswitch would have to call the Security
framework directly (e.g., via a small Swift helper, the Go `keyring`
package, or linking `libSecurity.dylib`). A future native rewrite will
do so; see the Go-rewrite proposal in the repo root.
