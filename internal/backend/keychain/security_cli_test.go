//go:build darwin

package keychain

import "testing"

// security -i exits 0 even when the command fails; the error must still surface.
func TestRunSecurityInteractive_ReportsFailure(t *testing.T) {
	if err := runSecurityInteractive("not-a-security-command\n"); err == nil {
		t.Fatal("want error for a failing security -i command, got nil")
	}
}
