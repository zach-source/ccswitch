package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend/inmem"
)

// fakeLister stands in for the macOS keychain backend: it reports orphaned
// hashed slots and records which ones were deleted.
type fakeLister struct {
	*inmem.Backend
	slots   []string
	deleted []string
}

func (f *fakeLister) ListByPrefix(context.Context, string) ([]string, error) { return f.slots, nil }

func (f *fakeLister) Delete(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	return nil
}

func TestPrune_ListsWithoutDeletingByDefault(t *testing.T) {
	f := &fakeLister{
		Backend: inmem.New(),
		slots:   []string{account.ActiveCredKey + "-03ad2101", account.ActiveCredKey + "-9127c670"},
	}

	out, err := capture(t, func() error { return runPrune(context.Background(), f, false) })
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(f.deleted) != 0 {
		t.Fatalf("prune deleted %v without --apply", f.deleted)
	}
	if !strings.Contains(out, "03ad2101") || !strings.Contains(out, "--apply") {
		t.Errorf("dry run should list the records and mention --apply:\n%s", out)
	}
}

func TestPrune_ApplyDeletesOnlyHashedSlots(t *testing.T) {
	f := &fakeLister{
		Backend: inmem.New(),
		slots:   []string{account.ActiveCredKey + "-03ad2101", account.ActiveCredKey + "-9127c670"},
	}

	if _, err := capture(t, func() error { return runPrune(context.Background(), f, true) }); err != nil {
		t.Fatalf("prune --apply: %v", err)
	}
	if len(f.deleted) != 2 {
		t.Fatalf("deleted = %v, want both hashed slots", f.deleted)
	}
	for _, d := range f.deleted {
		if d == account.ActiveCredKey {
			t.Fatal("prune deleted the live active slot")
		}
		if !strings.HasPrefix(d, account.ActiveCredKey+"-") {
			t.Errorf("prune deleted an unrelated key: %s", d)
		}
	}
}

func TestPrune_NoOrphansIsQuietSuccess(t *testing.T) {
	f := &fakeLister{Backend: inmem.New()}

	out, err := capture(t, func() error { return runPrune(context.Background(), f, true) })
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if !strings.Contains(out, "No orphaned") {
		t.Errorf("unexpected output:\n%s", out)
	}
}
