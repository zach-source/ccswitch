package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/config"
	"github.com/zach-source/ccswitch/internal/credentials"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newSyncCmd)
}

func newSyncCmd() *cobra.Command {
	var quiet bool

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Bi-directional sync between local and 1Password (newest-expiresAt-wins per item)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return err
			}
			return runSync(cfg, quiet)
		},
	}

	cmd.Flags().BoolVar(&quiet, "quiet", false, "Suppress informational output")
	return cmd
}

// runSync performs the bi-directional sync. Exported so daemon.go can call it.
func runSync(cfg *config.Config, quiet bool) error {
	log := func(format string, a ...any) {
		if !quiet {
			fmt.Printf("  "+format+"\n", a...)
		}
	}

	b, err := resolveBackend(cfg)
	if err != nil {
		return fmt.Errorf("backend not available: %w", err)
	}

	ctx := context.Background()
	if err := b.HealthCheck(ctx); err != nil {
		return fmt.Errorf("backend health check: %w", err)
	}

	sp := sequencePath()
	seq, err := account.LoadSequence(sp)
	if err != nil {
		return err
	}
	if len(seq.Sequence) == 0 {
		if !quiet {
			fmt.Println("No local sequence file")
		}
		return nil
	}

	if !quiet {
		fmt.Printf("Syncing with 1Password (vault: %s)...\n", cfg.OnePassword.Vault)
	}

	seqKey := fmt.Sprintf("%s - _sequence", cfg.OnePassword.ItemPrefix)

	// Sync sequence.json: newest lastUpdated wins.
	remoteSeqData, err := b.Read(ctx, seqKey)
	if err != nil {
		// No remote state — push local.
		log("No remote sequence; pushing local state")
		seqCopy := *seq
		seqCopy.SwitchLog = nil
		if data, err := json.MarshalIndent(seqCopy, "", "  "); err == nil {
			_ = b.Write(ctx, seqKey, data)
		}
	} else {
		var remoteSeq account.Sequence
		if err := json.Unmarshal(remoteSeqData, &remoteSeq); err == nil {
			local := seq.LastUpdated
			remote := remoteSeq.LastUpdated
			switch {
			case remote > local:
				log("Remote sequence newer (%s > %s); pulling", remote, local)
				remoteSeq.SwitchLog = seq.SwitchLog
				if remoteSeq.Accounts == nil {
					remoteSeq.Accounts = map[string]account.Account{}
				}
				if err := remoteSeq.Save(sp); err == nil {
					seq = &remoteSeq
				}
			case local > remote:
				log("Local sequence newer (%s > %s); pushing", local, remote)
				seqCopy := *seq
				seqCopy.SwitchLog = nil
				if data, err := json.MarshalIndent(seqCopy, "", "  "); err == nil {
					_ = b.Write(ctx, seqKey, data)
				}
			}
		}
	}

	pushed, pulled, nop := 0, 0, 0

	for _, id := range seq.Sequence {
		acct := seq.Accounts[id]
		itemKey := fmt.Sprintf("Claude Code Account - %s-%s", id, acct.Email)

		var localData []byte
		if id == seq.ActiveAccountID {
			localData, _ = b.Read(ctx, "Claude Code-credentials")
		} else {
			localData, _ = b.Read(ctx, itemKey)
		}
		remoteData, remoteErr := b.Read(ctx, itemKey)

		localExp := expiresAt(localData)
		remoteExp := expiresAt(remoteData)

		switch {
		case len(localData) > 0 && remoteErr != nil:
			// Local only — push.
			if err := b.Write(ctx, itemKey, localData); err == nil {
				log("%s %s: pushed (new in backend)", id, acct.Email)
				pushed++
			}
		case len(localData) == 0 && remoteErr == nil:
			// Remote only — pull.
			if id == seq.ActiveAccountID {
				_ = b.Write(ctx, "Claude Code-credentials", remoteData)
			}
			_ = b.Write(ctx, itemKey, remoteData)
			log("%s %s: pulled (new locally)", id, acct.Email)
			pulled++
		case localExp > remoteExp:
			if err := b.Write(ctx, itemKey, localData); err == nil {
				log("%s %s: pushed (local fresher)", id, acct.Email)
				pushed++
			}
		case remoteExp > localExp:
			if id == seq.ActiveAccountID {
				_ = b.Write(ctx, "Claude Code-credentials", remoteData)
			}
			_ = b.Write(ctx, itemKey, remoteData)
			log("%s %s: pulled (remote fresher)", id, acct.Email)
			pulled++
		default:
			nop++
		}
	}

	if !quiet {
		fmt.Printf("Sync complete: %d pushed, %d pulled, %d unchanged\n", pushed, pulled, nop)
	}
	return nil
}

// expiresAt extracts the expiresAt milliseconds from a credential blob, 0 if unparseable.
func expiresAt(data []byte) int64 {
	if len(data) == 0 {
		return 0
	}
	creds, err := credentials.Parse(data)
	if err != nil {
		return 0
	}
	return creds.ClaudeAIOAuth.ExpiresAtMillis
}

// logToFile appends msg to path, creating directories as needed.
func logToFile(path, msg string) {
	_ = os.MkdirAll(backupDir(), 0o700)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, msg)
}
