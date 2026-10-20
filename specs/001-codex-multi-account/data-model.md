# Data Model: Codex Multi-Account Switching

## Registry (`codex-profiles.json`, no secrets)

| Field | Type | Status | Rule |
|---|---|---|---|
| `version` | int | present | Stays `1`. The new fields are additive. |
| `active` | string | present | The profile that ccswitch last made live. Display only. A swap does not use it to find the live account. |
| `profiles` | map label → Profile | present | Key is the label. |
| `priority` | []string | **new** | Labels in switch order. Omit when empty. |
| `lastSwitch` | RFC 3339 string | **new** | Time of the last autoswitch. Used for the cooldown. |

`Registry.Ordered()` returns the labels in priority order. Labels that are not in `priority` come after it, in label order. Labels in `priority` that have no profile are skipped.

## Profile (in the registry)

| Field | Type | Rule |
|---|---|---|
| `label` | string | Unique. Default is the email. |
| `mode` | string | `chatgpt` or `apikey`. |
| `email` | string | From the `id_token` claims. |
| `plan` | string | From the `id_token` claims. |
| `accountId` | string | From `tokens.account_id`, or from the `id_token` auth claim. |

## Saved login (in the backend, secret)

- Key: `Codex-Account-<label>`.
- Value: the whole `auth.json` bytes. ccswitch never changes the bytes.
- A local fallback copy can exist under the same key in the local backend.

Fields that ccswitch reads (read only):

| Field | Use |
|---|---|
| `tokens.access_token` | Bearer token for usage. Its JWT `exp` sets "stale". |
| `tokens.account_id` | `ChatGPT-Account-Id` header, and identity match. |
| `tokens.id_token` | Email, plan and account ID claims. |
| `last_refresh` | Selects the fresher of the store copy and the local copy. |

## Usage (not stored)

| Field | Type | Rule |
|---|---|---|
| `status` | enum | `ok`, `stale`, `expired`, `unavailable`. |
| `windows` | []Window | From `rate_limit.primary_window` and `rate_limit.secondary_window`. Null windows are skipped. |
| `plan` | string | From `plan_type`. |

Window: `seconds` (from `limit_window_seconds`), `usedPercent`, `resetAt` (from `reset_at`, Unix seconds).

Window names: 18000 seconds is `5h`. 604800 seconds is `weekly`. Any other length shows as hours.

Load of a profile: the highest `usedPercent` of its windows. A profile with status other than `ok` has no load.

## State changes

```text
codex login <email>  → new Profile + saved login, label added to the end of priority
codex use <label>    → live login saved to the profile that matches its account,
                       then the target login written live, active = label
codex autoswitch     → same as use, plus lastSwitch = now
codex remove <label> → Profile and saved login deleted, label removed from priority
```
