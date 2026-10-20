# Research: Codex Multi-Account Switching

Sources: the Codex source at tag `rust-v0.160.1` (commit `d27764b`), live tests on this Mac, and the source of eight open-source Codex switchers. Paths below are relative to `codex-rs/`.

## R1. Codex rotates refresh tokens

- **Decision**: ccswitch never renews a Codex login itself.
- **Rationale**: `login/src/auth/manager.rs:1599` (`persist_tokens`) saves the new refresh token after each renewal. The old token can then stop working.

- Codex reports `refresh_token_reused`, `refresh_token_invalidated` or `invalid_grant` as permanent errors (`manager.rs:1679-1697`). If ccswitch renews a token and then fails to save it, the account must sign in again.

- Claude had this exact failure. v2.10.1 fixed it.
- **Alternatives considered**: renew inactive profiles under a lock, as `ccswap` does. Rejected: an access token lasts 10 days. The benefit is small. The risk is that the account must sign in again.

## R2. Access token life

- **Decision**: usage for a saved profile uses its saved access token until the token's `exp`. After that, the profile shows "stale".
- **Rationale**: The live access token has `exp - iat` = 240 hours (10 days). Codex renews it when it is 5 minutes from `exp` (`manager.rs:204`, `:3004-3026`).

## R3. A running Codex after a swap

- **Decision**: a switch affects new sessions only. ccswitch identifies the live login by its account ID and email, never by the profile it last made active.
- **Rationale**: On a 401, a running Codex reloads `auth.json` only if the account ID is the same (`manager.rs:2487-2520`). For a different account, it fails and does not adopt the file.

- But `persist_tokens` loads the file, overlays its own tokens and writes it. It does no compare and takes no lock (`storage.rs:208-225`). So an old session that renews after a swap can write its own tokens into the live file.

- The current `snapshotLiveCodex` (`internal/cli/codex.go`) saves the live file into `reg.Active`. This can save one account's login under the other account.

- **Alternatives considered**: One `CODEX_HOME` for each account (the `codex-profiles` method). Rejected: it divides sessions, memories and config between accounts.

- Stop and start the Codex app-server after a swap (the `mcdx` method). Rejected for now: it stops the user's running sessions.

## R4. SessionStart hook timing

- **Decision**: run `ccswitch codex autoswitch --once` from the Codex `SessionStart` hook. The switch is for the next session.
- **Rationale**: The hook runs at the first turn, before the first model request. Codex waits for it (`core/src/hook_runtime.rs:128-180`).

- Codex loaded the login before that. So the current session keeps its account (`core/src/session/turn.rs:322`).

## R5. Browser for sign-in

- **Decision**: use `codex login --device-auth`. ccswitch reads the address and the one-time code from the output. It opens the address in the Chrome profile for the email.

- **Rationale**: `codex login` opens the browser with the `webbrowser` crate 1.2.4. On macOS, that crate calls NSWorkspace. It does not call the `open` command (`login/src/server.rs:203-205`). So the `PATH` shim that Claude login uses has no effect.

- Device sign-in prints the address `{base}/codex/device`. It also prints a code that expires in 15 minutes (`login/src/device_code_auth.rs:149-187`).

- If device sign-in is not available, Codex falls back to the browser sign-in (`cli/src/login.rs:410-418`).

- **Alternatives considered**: parse the browser sign-in address from the output. Rejected: Codex always opens the default browser too. It has no flag to stop it. So the page opens two times.

## R6. Sign in without a change to the live login

- **Decision**: run the sign-in with `CODEX_HOME` set to a new temporary directory. Delete the directory after ccswitch saves the login. Do not run `codex logout`. It revokes the tokens.

- **Rationale**: Codex reads `CODEX_HOME` (`utils/home-dir/src/lib.rs:14-25`). Before a sign-in, Codex revokes only the login in that directory (`cli/src/login.rs:122-150`).

- A second sign-in on port 1455 cancels the first (`login/src/server.rs:618-690`). So ccswitch runs one sign-in at a time.

## R7. Usage data

- **Decision**: send `GET https://chatgpt.com/backend-api/wham/usage` with `Authorization: Bearer <access_token>` and `ChatGPT-Account-Id: <account_id>`. Map each window by `limit_window_seconds`. 18000 is 5 hours. 604800 is weekly.

- **Rationale**: The live test returned HTTP 200. The `ChatGPT-Account-Id` header was not necessary. Codex sends it (`backend-client/src/client.rs:269-288`), so ccswitch sends it too.

- This Pro account reports only a weekly window, in `primary_window`. `secondary_window` is null. So the position of a window does not show its length.

- The response struct is in `codex-backend-openapi-models/src/models/rate_limit_status_payload.rs:16-53`.

- **Alternatives considered**: the app-server JSON-RPC call `account/rateLimits/read` with `refreshToken: false`. Rejected for now: it needs a running app-server for each `CODEX_HOME`.

- The `rate_limits` in session files. They give data only for the live account.

## R8. Login store mode

- **Decision**: support the file store only. Keep the current refusal for keyring mode.
- **Rationale**: the default is `file` (`config/src/types.rs:113-125`). The user's `config.toml` does not set `cli_auth_credentials_store`.

## R9. ChatGPT desktop app

- **Decision**: no special handling now. Check after the first real switch.
- **Rationale**: The app bundles its own Codex (0.163.0-alpha). Its binary does not contain the string `auth.json`. So it probably keeps its own login. This is not confirmed.

## Open item to check during the work

- The exact format of the one-time code line in the device sign-in output. The parser must accept the format that `device_code_auth.rs:149-158` prints.
- If the `stigen.ai` workspace blocks device sign-in, Codex falls back to the browser sign-in. ccswitch must then tell the user that the default browser opened.
