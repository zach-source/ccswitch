# Feature Specification: Codex Multi-Account Switching

**Feature Branch**: `001-codex-multi-account`

**Created**: 2026-10-20

**Status**: Draft

**Input**: User description: "Add Codex support for multiple accounts, the same as for Claude. The primary account is ztaylor@stigen.ai. The secondary account is zctaylor.work@gmail.com. Login must open the browser in the correct profile. Claude login does this too."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Log in each Codex account in its own browser profile (Priority: P1)

The user runs one command for each Codex account. The sign-in page opens in the Chrome profile that is signed in to that account's email. When the sign-in completes, ccswitch saves the login as a profile. The live Codex login does not change.

**Why this priority**: No other story works without saved profiles. Today the user has none. A sign-in that opens in the wrong browser profile signs in to the wrong account.

**Independent Test**: Run the login command for `ztaylor@stigen.ai`. Confirm that the sign-in page opens in the matching Chrome profile. Confirm that the saved profile shows that email. Confirm that the live Codex login is the same as before.

**Acceptance Scenarios**:

1. **Given** a Chrome profile is signed in to `ztaylor@stigen.ai`. **When** the user starts the login for that email, **Then** the sign-in page opens in that Chrome profile. The user sees the one-time code in the terminal.
2. **Given** the sign-in is complete. **When** ccswitch reads the new login, **Then** it saves the login as a profile. This happens only if the login's email is the requested email.
3. **Given** the sign-in is complete for a different email. **When** ccswitch reads the new login, **Then** it discards the login. It saves nothing. It tells the user which email signed in.
4. **Given** a live Codex login exists before the command. **When** the login command finishes or fails, **Then** ccswitch does not change the live login.
5. **Given** no Chrome profile matches the email. **When** the user starts the login, **Then** ccswitch shows the sign-in address and code. It tells the user to open the address in the correct browser.

---

### User Story 2 - See usage for every Codex account (Priority: P2)

The user runs one command. The user sees, for each saved Codex profile, the used percent of each usage window. The user also sees when each window resets.

**Why this priority**: The user cannot choose an account without usage data. Autoswitch cannot act without it either.

**Independent Test**: With two saved profiles, run the usage command. Confirm that there is one row for each profile. Confirm that each row shows the used percent and reset time of each window that the account reports.

**Acceptance Scenarios**:

1. **Given** two saved profiles with valid logins. **When** the user runs the usage command, **Then** each profile shows the used percent and reset time of each window.
2. **Given** an account that reports only a weekly window. **When** usage shows, **Then** ccswitch shows the weekly window. It does not show an empty 5-hour window.
3. **Given** a saved profile whose access has expired. **When** usage shows, **Then** that profile shows "stale". ccswitch does not renew its login.
4. **Given** the usage service is unreachable or rate-limited. **When** usage shows, **Then** that profile shows "unavailable". ccswitch does not mark any profile as expired because of it.

---

### User Story 3 - Switch Codex accounts automatically by priority (Priority: P3)

When the primary account (`ztaylor@stigen.ai`) reaches the usage threshold, the next new Codex session uses the secondary account (`zctaylor.work@gmail.com`). When the primary account falls below the threshold after a reset, the next new session uses the primary account again.

**Why this priority**: This removes manual work. It depends on stories 1 and 2.

**Independent Test**: With the primary account over the threshold, start a Codex session. Confirm that the session after it uses the secondary account. With the primary account under the threshold, confirm that the next session uses the primary account again.

**Acceptance Scenarios**:

1. **Given** the primary account is at or over the threshold. The secondary account is under it. **When** a Codex session starts, **Then** the live login changes to the secondary account. This applies to the next session.
2. **Given** the secondary account is live and the primary account is under the threshold, **When** a Codex session starts, **Then** the live login changes back to the primary account.
3. **Given** usage for the live account is unavailable, **When** a Codex session starts, **Then** no switch occurs.
4. **Given** all accounts are at or over the threshold, **When** a Codex session starts, **Then** no switch occurs.
5. **Given** two sessions start at the same time, **When** both run the check, **Then** at most one switch occurs.

---

### User Story 4 - Never lose a rotated Codex login (Priority: P1)

When ccswitch replaces the live Codex login, it first saves the live login into the profile of the same account. It never files the login of one account under a different account.

**Why this priority**: If ccswitch files a login under the wrong account, or loses a renewed login, the user must sign in again. This is the same failure that the user reported for Claude.

**Independent Test**: Make the live login belong to account B. Make the registry show that account A is active. Run a switch. Confirm that the profile of account B gets the live login. Confirm that the profile of account A is unchanged.

**Acceptance Scenarios**:

1. **Given** the live login belongs to a saved profile. **When** a switch starts, **Then** ccswitch saves the live login into that profile. It does this before it replaces the live login.
2. **Given** the live login belongs to an account that matches no saved profile, **When** a switch starts, **Then** ccswitch stops. It changes nothing. It tells the user to save that login first.
3. **Given** the save to the profile store fails. **When** a switch starts, **Then** ccswitch keeps a local copy of the live login. It does not lose the live login.

### Edge Cases

- An old Codex session that still runs can renew its login after a switch. It can then write its account back into the live login. The next switch must identify the live login by its account. It must not use the account that ccswitch last made active.
- A second sign-in that starts while one is open cancels the first. ccswitch must not start two sign-ins at the same time.
- If the workspace for `ztaylor@stigen.ai` does not allow sign-in by one-time code, ccswitch must fall back to the normal sign-in. The normal sign-in opens the default browser. ccswitch must say so.
- If a Codex setup keeps its login in the OS keyring instead of a file, ccswitch refuses. This is the same as today.
- The ChatGPT desktop app runs its own Codex all the time. It can share the live login or not.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Users MUST be able to sign in a Codex account by email with one command. The sign-in page MUST open in the Chrome profile that is signed in to that email.
- **FR-002**: The sign-in MUST NOT change or revoke the live Codex login.
- **FR-003**: The system MUST save a new login as a profile only when the login's email equals the requested email.
- **FR-004**: The system MUST keep an ordered priority list of Codex profiles. The default order for this user is `ztaylor@stigen.ai`, then `zctaylor.work@gmail.com`.
- **FR-005**: Users MUST be able to see, for each profile, the used percent and reset time of each usage window that the account reports.
- **FR-006**: The system MUST identify each usage window by its length, not by its position.
- **FR-007**: The system MUST NOT renew a Codex login itself. A profile whose access has expired shows "stale".
- **FR-008**: Before it replaces the live login, the system MUST save the live login into the matching profile. If no profile matches, the system MUST stop.
- **FR-009**: If the write to the profile store fails, the system MUST keep the live login in a local copy. Reads MUST use the copy that expires later.
- **FR-010**: Automatic switching MUST select the highest-priority profile under the threshold. It MUST act only when that profile differs from the live one.
- **FR-011**: Automatic switching MUST NOT act on unavailable usage data. It MUST allow only one run at a time. It MUST respect a cooldown after a switch.
- **FR-012**: Automatic switching MUST run when a Codex session starts. It MUST affect only later sessions.
- **FR-013**: The system MUST treat "refresh token reused", "invalidated" and "invalid grant" results as "sign in again". It MUST NOT retry them.

### Key Entities

- **Codex profile**: one saved Codex login. Attributes: label, email, account ID, plan, priority position. The login itself is in the profile store. It is never in the registry.
- **Usage window**: one usage limit for an account. Attributes: length, used percent, reset time.
- **Live login**: the single Codex login that new Codex sessions use.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: The user can sign in to both Codex accounts in under 3 minutes in total. The user does not change the browser profile by hand.
- **SC-002**: In 30 days of use, no switch causes a Codex "sign in again" error for a profile. That profile was valid before the switch.
- **SC-003**: The usage command shows all profiles in under 5 seconds.
- **SC-004**: When the primary account is over the threshold, the next new Codex session uses the secondary account. This happens in 100% of tested cases.
- **SC-005**: In tests that simulate a renewal after a switch, no login is ever filed under a different account.

## Assumptions

- The user keeps one Chrome profile signed in to each account's Google email, the same as for Claude login.
- The Codex login is kept in a file, not in the OS keyring. This is the default. The user's config does not change it.
- The default threshold is 80% and the default cooldown is 5 minutes, the same as Claude autoswitch.
- An access token is valid for about 10 days. A profile that is unused for longer shows "stale" until the user uses it again or signs in again.
- A running Codex session keeps its account until it restarts. A switch affects only new sessions.
- The ChatGPT desktop app probably does not share the live login. The first real switch confirms this.
