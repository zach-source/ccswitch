package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/config"
	"github.com/zach-source/ccswitch/internal/credentials"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newSwitchCmd)
	subcommandBuilders = append(subcommandBuilders, newSwitchToCmd)
}

func newSwitchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "switch",
		Short: "Interactively pick an account to switch to (fzf if available, else numbered prompt)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return err
			}

			seq, err := account.LoadSequence(sequencePath())
			if err != nil {
				return err
			}
			if len(seq.Sequence) == 0 {
				return fmt.Errorf("no accounts are managed yet")
			}

			targetID, err := pickAccountInteractive(seq)
			if err != nil {
				return err
			}
			return performSwitch(cmd, cfg, seq, targetID)
		},
	}
}

func newSwitchToCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "switch-to <hash|email|index>",
		Short: "Non-interactively switch to a specific account by hash, email, or 1-based index",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return err
			}

			seq, err := account.LoadSequence(sequencePath())
			if err != nil {
				return err
			}
			if len(seq.Sequence) == 0 {
				return fmt.Errorf("no accounts are managed yet")
			}

			id := seq.Resolve(args[0])
			if id == "" {
				return fmt.Errorf("no account found matching: %s", args[0])
			}
			return performSwitch(cmd, cfg, seq, id)
		},
	}
}

// pickAccountInteractive shows an fzf picker when fzf is on PATH, otherwise a
// numbered text prompt.
func pickAccountInteractive(seq *account.Sequence) (string, error) {
	active := activeID(seq)
	lines := make([]string, len(seq.Sequence))
	for i, id := range seq.Sequence {
		acct := seq.Accounts[id]
		marker := ""
		if id == active {
			marker = " (active)"
		}
		lines[i] = fmt.Sprintf("%s  %s  [%s]%s", id, acct.Email, displayOrg(acct.OrgName), marker)
	}

	if _, err := exec.LookPath("fzf"); err == nil {
		return pickWithFzf(seq.Sequence, lines)
	}
	return pickWithPrompt(seq.Sequence, lines)
}

func pickWithFzf(ids []string, lines []string) (string, error) {
	input := strings.Join(lines, "\n")
	fzf := exec.Command("fzf", "--height=40%", "--reverse", "--prompt=Account> ")
	fzf.Stdin = strings.NewReader(input)
	fzf.Stderr = os.Stderr
	out, err := fzf.Output()
	if err != nil {
		return "", fmt.Errorf("fzf cancelled or failed: %w", err)
	}
	selected := strings.TrimSpace(string(out))
	// Match by prefix (hash is the first 8-char field).
	for i, line := range lines {
		if line == selected {
			return ids[i], nil
		}
	}
	return "", fmt.Errorf("could not match selection")
}

func pickWithPrompt(ids []string, lines []string) (string, error) {
	fmt.Println("Select account:")
	for i, line := range lines {
		fmt.Printf("  %d) %s\n", i+1, line)
	}
	fmt.Print("Enter number: ")
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	n, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil || n < 1 || n > len(ids) {
		return "", fmt.Errorf("invalid selection")
	}
	return ids[n-1], nil
}

// performSwitch activates the target account: copies the target's backup
// credentials into the active slot of the local backend, updates
// sequence.json, and prints the post-switch instructions.
//
// Order of operations is chosen to be recovery-safe:
//  1. Read target's backup creds (fail fast if missing).
//  2. Snapshot the prior active account's creds into its backup slot.
//  3. Save sequence.json (so a crash here leaves keychain inconsistent
//     with sequence.json — but sequence.json is the cheap thing to fix).
//  4. Merge target's tokens into the active slot.
//  5. Write target's identity to ~/.claude.json, rolling back step 4 on
//     failure so the token and identity never disagree.
//
// If step 4 crashes, sequence.json points to the new account but the
// active slot still has the old one — `ccswitch save` recovers.
// Doing step 4 before step 3 would leave the user with new creds in the
// active slot but sequence.json still naming the prior account, which
// is the harder direction to detect.
//
// Steps 2 and 4 go through credentials.OAuthOnly / credentials.SwapOAuth
// rather than copying whole blobs: only claudeAiOauth belongs to an account.
// Sibling keys — mcpOAuth, holding every MCP server's authorization — are
// account-independent and stay with the active slot across a switch.
func performSwitch(cmd *cobra.Command, cfg *config.Config, seq *account.Sequence, targetID string) error {
	acct, ok := seq.Accounts[targetID]
	if !ok {
		return fmt.Errorf("account %s not found", targetID)
	}

	// Two backend roles. The active slot (ActiveCredKey) is always the local
	// store — keychain on macOS — because that is what `claude` actually
	// reads. Per-account backups (BackupCredKey) live in the *configured*
	// backend, which may be remote (1Password); that is where `login` and
	// `save` write them. When the configured backend is the local one these
	// are the same object, and the code below still works.
	store, err := resolveBackend(cfg)
	if err != nil {
		return fmt.Errorf("resolve backend: %w", err)
	}
	local, err := resolveLocalBackend()
	if err != nil {
		return fmt.Errorf("resolve local backend: %w", err)
	}

	ctx := cmd.Context()

	// 1. Read target's backup creds from the store; useful hint on miss.
	targetData, err := store.Read(ctx, account.BackupCredKey(targetID, acct.Email))
	if err != nil {
		if errors.Is(err, backend.ErrNotFound) {
			return fmt.Errorf("no stored credentials for %s (%s); run `ccswitch login --only %s` first",
				targetID, acct.Email, targetID)
		}
		return fmt.Errorf("read target creds: %w", err)
	}

	// Read the live active slot once. It serves three purposes: the prior
	// account's snapshot (step 2), the carrier for the target's tokens
	// (step 4), and the rollback value if a later step fails.
	liveActive, rerr := local.Read(ctx, account.ActiveCredKey)
	if rerr != nil && !errors.Is(rerr, backend.ErrNotFound) {
		fmt.Fprintf(os.Stderr, "Warning: could not read prior active creds: %v\n", rerr)
	}

	// 2. Snapshot prior active into its backup slot in the store (best-effort).
	// The prior-active account is taken from the live .claude.json, not
	// sequence.json's recorded activeAccountId — the recorded value can be
	// stale, and snapshotting the active slot under the wrong account's
	// backup key would file the credentials against the wrong identity.
	priorID := activeID(seq)
	if priorID != "" && priorID != targetID {
		if cur, ok := seq.Accounts[priorID]; ok {
			if len(liveActive) > 0 {
				// Strip to claudeAiOauth. mcpOAuth and any other sibling key
				// is account-independent: filing it under this account would
				// smear one account's MCP authorizations onto the next.
				snapshot, oerr := credentials.OAuthOnly(liveActive)
				if oerr != nil {
					fmt.Fprintf(os.Stderr, "Warning: could not snapshot prior active creds: %v\n", oerr)
				} else if werr := store.Write(ctx, account.BackupCredKey(priorID, cur.Email), snapshot); werr != nil {
					fmt.Fprintf(os.Stderr, "Warning: could not snapshot prior active creds: %v\n", werr)
				}
			}
			// Also snapshot the prior identity block. The freshly-read live
			// identity is the right thing to file under the prior account —
			// the recorded OAuthAccount on the sequence can be older. Saved
			// to disk along with ActiveAccountID in step 3 below.
			if block, berr := readClaudeOAuthBlock(); berr == nil && len(block) > 0 {
				cur.OAuthAccount = block
				seq.Accounts[priorID] = cur
			}
		}
	}

	// 3. Save sequence.json before mutating the active slot.
	seq.ActiveAccountID = targetID
	seq.SwitchLog = append(seq.SwitchLog, account.SwitchLogEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		To:        targetID,
	})
	if err := seq.Save(sequencePath()); err != nil {
		return fmt.Errorf("save sequence: %w", err)
	}

	// 4. Write target into active slot — merged, not wholesale. targetData is
	// a per-account backup holding only claudeAiOauth; writing it verbatim
	// would delete the live blob's mcpOAuth and force every MCP server to be
	// re-authorized after every switch.
	activeData, err := credentials.SwapOAuth(liveActive, targetData)
	if err != nil {
		return fmt.Errorf("prepare active slot: %w", err)
	}
	if err := local.Write(ctx, account.ActiveCredKey, activeData); err != nil {
		return fmt.Errorf("write active slot: %w", err)
	}

	// 5. Restore target's identity into ~/.claude.json. Without this only the
	// OAuth token swaps; Claude Code keeps showing the previous account
	// because it reads `oauthAccount` (email, accountUuid, organizationUuid,
	// displayName, ...) from the JSON file, not from the token.
	//
	// Source order: the per-account block captured at add-account/save time,
	// then a legacy ~/.claude-switch-backup/configs/.claude-config-<id>-<email>.json
	// snapshot (left over from the shell ccswitch.sh) so accounts added
	// before this feature get a working switch without re-running save.
	identity := acct.OAuthAccount
	if len(identity) == 0 {
		legacy := filepath.Join(backupDir(), "configs",
			fmt.Sprintf(".claude-config-%s-%s.json", targetID, acct.Email))
		if data, rerr := os.ReadFile(legacy); rerr == nil {
			var top map[string]json.RawMessage
			if json.Unmarshal(data, &top) == nil {
				if block, ok := top["oauthAccount"]; ok && len(block) > 0 {
					identity = block
					// Persist forward so the legacy snapshot is no longer needed.
					acct.OAuthAccount = block
					seq.Accounts[targetID] = acct
					_ = seq.Save(sequencePath())
				}
			}
		}
	}
	if len(identity) > 0 {
		if err := writeClaudeOAuthBlock(identity); err != nil {
			// The token is already swapped but the identity is not. That
			// pairing is the whole point of a switch (see #10), so undo the
			// token write rather than leave the two halves disagreeing.
			if len(liveActive) > 0 {
				if rberr := local.Write(ctx, account.ActiveCredKey, liveActive); rberr != nil {
					fmt.Fprintf(os.Stderr, "Warning: rollback of the active slot failed: %v\n", rberr)
				}
			}
			return fmt.Errorf("write target identity to ~/.claude.json: %w", err)
		}
	} else {
		fmt.Fprintf(os.Stderr,
			"Warning: no stored identity for %s (%s). The OAuth token was swapped,\n"+
				"but Claude Code will keep showing the previous account on restart.\n"+
				"Run `ccswitch save` once while logged in as %s to capture its\n"+
				"identity for future switches.\n",
			targetID, acct.Email, acct.Email)
	}

	fmt.Printf("Switched to %s (%s)\n", targetID, acct.Email)
	fmt.Println()
	fmt.Println("Please restart Claude Code to use the new authentication.")
	fmt.Println()
	return nil
}
