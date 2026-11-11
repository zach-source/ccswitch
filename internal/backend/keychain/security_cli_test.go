//go:build darwin

package keychain

import (
	"context"
	"strings"
	"testing"
)

// security -i exits 0 even when the command fails; the error must still surface.
func TestRunSecurityInteractive_ReportsFailure(t *testing.T) {
	if err := runSecurityInteractive("not-a-security-command\n"); err == nil {
		t.Fatal("want error for a failing security -i command, got nil")
	}
}

// security echoes a bad command line. The error must not repeat hex from it.
func TestRunSecurityInteractive_HidesHex(t *testing.T) {
	err := runSecurityInteractive("0123456789abcdef0123456789abcdef\n")
	if err == nil || strings.Contains(err.Error(), "0123456789abcdef") {
		t.Fatalf("err = %v, want an error without the hex", err)
	}
}

// A value that does not fit on one security -i line must fail before security
// runs, so no cut-off value reaches the keychain.
func TestWrite_RefusesValueTooLarge(t *testing.T) {
	err := New().Write(context.Background(), "ccswitch-test-too-large", make([]byte, 2100))
	if err == nil || !strings.Contains(err.Error(), "2100 bytes") {
		t.Fatalf("err = %v, want a size error", err)
	}
}
