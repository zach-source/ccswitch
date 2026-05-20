package refresh

import (
	"context"
	"testing"
	"time"

	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend/inmem"
)

// hashedFake wraps an inmem backend so it also satisfies hashedSlotLookup,
// returning a fixed service name + payload — the shape the macOS keychain
// backend produces for claude 2.x's "Claude Code-credentials-<hash>" slot.
type hashedFake struct {
	*inmem.Backend
	svc  string
	data []byte
}

func (h *hashedFake) LookupHashedActiveSlot(_ context.Context, _ time.Time) ([]byte, string, error) {
	return h.data, h.svc, nil
}

// TestCaptureClaudeCredential_ReturnsHashedService pins the contract the
// orphan cleanup depends on: when the credential is captured from the hashed
// keychain slot (the file and active-slot sources both miss), capture must
// return that slot's service name so the caller can delete the throwaway
// record. A regression here is exactly what let dozens of
// "Claude Code-credentials-<hash>" items pile up in the login keychain.
func TestCaptureClaudeCredential_ReturnsHashedService(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir() // no .credentials.json present -> file source misses

	const svc = "Claude Code-credentials-deadbeef"
	payload := []byte(`{"claudeAiOauth":{"accessToken":"AT","refreshToken":"RT","expiresAt":1}}`)

	// Empty active slot -> active-slot source misses, forcing the hashed branch.
	fake := &hashedFake{Backend: inmem.New(), svc: svc, data: payload}
	got, gotSvc := captureClaudeCredential(ctx, tmp, nil, fake, nil, time.Now())
	if string(got) != string(payload) {
		t.Fatalf("data mismatch: want %s got %s", payload, got)
	}
	if gotSvc != svc {
		t.Fatalf("service name: want %q got %q (the orphan would never be deleted)", svc, gotSvc)
	}

	// When the credential comes from the active slot instead, no hashed
	// service name is returned (nothing to delete).
	active := &hashedFake{Backend: inmem.New(), svc: svc, data: payload}
	if err := active.Write(ctx, account.ActiveCredKey, []byte(`{"claudeAiOauth":{"accessToken":"new"}}`)); err != nil {
		t.Fatal(err)
	}
	if _, svc2 := captureClaudeCredential(ctx, tmp, nil, active, nil, time.Now()); svc2 != "" {
		t.Fatalf("active-slot capture must return empty service, got %q", svc2)
	}
}
