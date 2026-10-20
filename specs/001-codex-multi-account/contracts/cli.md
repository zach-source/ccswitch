# CLI Contract: `ccswitch codex`

Existing commands (`add`, `use`, `list`, `current`, `remove`) keep their arguments. This file lists new commands and changed behavior.

## `ccswitch codex login <email> [--label <label>]` (new)

| Item | Contract |
|---|---|
| Effect | Signs in `<email>` in a temporary `CODEX_HOME`, and saves the login as profile `<label>` (default: `<email>`). |
| Live login | Not changed. |
| Browser | Opens the device sign-in address in the Chrome profile signed in to `<email>`. If no profile matches, prints the address. |
| Output | The one-time code, the address, and the result line `Saved Codex profile "<label>": <email> (<plan>)`. |
| Exit 0 | The login saved. |
| Exit non-zero | `codex` failed or was not found. Or the signed-in email is not `<email>`: the message names the email that signed in. Or another sign-in is running. |

## `ccswitch codex priority [<label>...]` (new)

- With labels: sets the priority order. Each label must be a saved profile.
- With no labels: prints the order, one label on each line.

## `ccswitch codex usage [--json]` (new)

Text output, one row for each profile in priority order:

```text
* ztaylor@stigen.ai        pro   weekly 41% (resets Oct 27 01:20)
  zctaylor.work@gmail.com  plus  5h 12% (resets 18:05)  weekly 30% (resets Oct 24 09:00)
  old@example.com          -     stale (last used Oct 02)
```

`*` marks the live profile. `--json` prints an array of objects with `label`, `email`, `live`, `status`, `plan`, `windows` (`seconds`, `usedPercent`, `resetAt`).

Exit 0 when the command ran. A profile's failure shows in its row, not in the exit code.

## `ccswitch codex autoswitch --once [--threshold 80] [--cooldown 5m]` (new)

- Prints one line: `autoswitch: <from> <load>% -> <to> <load>%`, or `no action: <reason>`.
- Reasons: `live usage unavailable`, `cooldown active`, `another codex autoswitch is running`, `no profile under threshold`, `live profile is the best choice`, `live login matches no profile`.
- Exit 0 for a switch or no action. Exit non-zero only for a failed write.

## `ccswitch codex use <label>` (changed)

- Before the swap, it saves the live login into the profile whose account matches the live login. Before this change, it saved into `active`.
- If the live login matches no profile, it stops with exit non-zero and changes nothing. The message tells the user to run `ccswitch codex add <label>` first.
- If the store write fails, it writes a local copy and continues. It prints a warning.
