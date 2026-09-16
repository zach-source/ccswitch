package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/account"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newPruneCmd)
}

// hashedSlotLister is implemented by the macOS keychain backend. Other
// backends cannot accumulate these records, so prune is a no-op there.
type hashedSlotLister interface {
	ListByPrefix(ctx context.Context, prefix string) ([]string, error)
	Delete(ctx context.Context, key string) error
}

func newPruneCmd() *cobra.Command {
	var apply bool

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete orphaned \"Claude Code-credentials-<hash>\" keychain records left by past logins",
		Long: "Each interactive login makes claude write its credential to a\n" +
			"per-CLAUDE_CONFIG_DIR keychain record named \"Claude Code-credentials-<hash>\".\n" +
			"ccswitch captures from that record and deletes it, but logins from before\n" +
			"that cleanup existed left theirs behind — and each one is a separate\n" +
			"keychain item with its own access-control list, so they add up to real\n" +
			"prompt noise.\n\n" +
			"Only the suffixed records are touched. The live active slot\n" +
			"(\"Claude Code-credentials\", no suffix) and every per-account backup are\n" +
			"left alone.\n\n" +
			"Lists what it would delete by default; pass --apply to actually delete.",
		RunE: func(cmd *cobra.Command, args []string) error {
			local, err := resolveLocalBackend()
			if err != nil {
				return fmt.Errorf("local backend not available: %w", err)
			}
			lister, ok := local.(hashedSlotLister)
			if !ok {
				fmt.Printf("Nothing to prune: the %s backend does not accumulate these records.\n", local.Name())
				return nil
			}

			return runPrune(cmd.Context(), lister, apply)
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "Actually delete the records (default: list only)")
	return cmd
}

// runPrune lists, and with apply set deletes, every orphaned hashed slot.
func runPrune(ctx context.Context, lister hashedSlotLister, apply bool) error {
	slots, err := lister.ListByPrefix(ctx, account.ActiveCredKey+"-")
	if err != nil {
		return fmt.Errorf("list orphaned records: %w", err)
	}
	if len(slots) == 0 {
		fmt.Println("No orphaned credential records found.")
		return nil
	}

	if !apply {
		fmt.Printf("%d orphaned credential record(s) would be deleted:\n\n", len(slots))
		for _, s := range slots {
			fmt.Printf("  %s\n", s)
		}
		fmt.Println("\nRe-run with --apply to delete them.")
		return nil
	}

	deleted, failed := 0, 0
	for _, s := range slots {
		if err := lister.Delete(ctx, s); err != nil {
			fmt.Fprintf(os.Stderr, "  could not delete %s: %v\n", s, err)
			failed++
			continue
		}
		deleted++
	}
	fmt.Printf("Deleted %d orphaned credential record(s).\n", deleted)
	if failed > 0 {
		return fmt.Errorf("prune: %d record(s) could not be deleted", failed)
	}
	return nil
}
