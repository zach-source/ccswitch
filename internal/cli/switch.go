package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/config"
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
	lines := make([]string, len(seq.Sequence))
	for i, id := range seq.Sequence {
		acct := seq.Accounts[id]
		org := acct.OrgName
		if strings.HasSuffix(org, "'s Organization") {
			org = "Personal"
		}
		if org == "" {
			org = "Unknown"
		}
		marker := ""
		if id == seq.ActiveAccountID {
			marker = " (active)"
		}
		lines[i] = fmt.Sprintf("%s  %s  [%s]%s", id, acct.Email, org, marker)
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
func performSwitch(cmd *cobra.Command, cfg *config.Config, seq *account.Sequence, targetID string) error {
	acct, ok := seq.Accounts[targetID]
	if !ok {
		return fmt.Errorf("account %s not found", targetID)
	}

	localCfg := *cfg
	localCfg.Backend = autoLocalBackend()
	local, err := resolveBackend(&localCfg)
	if err != nil {
		return fmt.Errorf("resolve local backend: %w", err)
	}

	ctx := cmd.Context()

	// Save the currently-active account's creds into its own backup slot
	// before overwriting the active slot — preserves "save before switch".
	if seq.ActiveAccountID != "" && seq.ActiveAccountID != targetID {
		if cur, ok := seq.Accounts[seq.ActiveAccountID]; ok {
			if data, err := local.Read(ctx, account.ActiveCredKey); err == nil && len(data) > 0 {
				_ = local.Write(ctx, account.BackupCredKey(seq.ActiveAccountID, cur.Email), data)
			}
		}
	}

	// Copy the target account's backup creds into the active slot.
	data, err := local.Read(ctx, account.BackupCredKey(targetID, acct.Email))
	if err != nil {
		return fmt.Errorf("read target creds: %w", err)
	}
	if err := local.Write(ctx, account.ActiveCredKey, data); err != nil {
		return fmt.Errorf("write active slot: %w", err)
	}

	seq.ActiveAccountID = targetID
	seq.SwitchLog = append(seq.SwitchLog, account.SwitchLogEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		To:        targetID,
	})
	if err := seq.Save(sequencePath()); err != nil {
		return fmt.Errorf("save sequence: %w", err)
	}

	fmt.Printf("Switched to %s (%s)\n", targetID, acct.Email)
	fmt.Println()
	fmt.Println("Please restart Claude Code to use the new authentication.")
	fmt.Println()
	return nil
}
