package onepassword

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsPermissionDenied(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"403", &httpError{status: 403}, true},
		{"wrapped 403", fmt.Errorf("update %q: %w", "acct", &httpError{status: 403}), true},
		{"404", &httpError{status: 404}, false},
		{"500", &httpError{status: 500}, false},
		{"other error", errors.New("boom"), false},
	}
	for _, c := range cases {
		if got := isPermissionDenied(c.err); got != c.want {
			t.Errorf("%s: isPermissionDenied = %v, want %v", c.name, got, c.want)
		}
	}
}

// Write must go to the op CLI even when Connect can write: Connect cannot
// update the op documents that hold the credentials. A nil primary panics if
// Write touches Connect.
func TestCLIFallback_WriteUsesCLI(t *testing.T) {
	dir := t.TempDir()
	args, stdin := filepath.Join(dir, "args"), filepath.Join(dir, "stdin")
	op := filepath.Join(dir, "op")
	script := "#!/bin/sh\necho \"$@\" > '" + args + "'\ncat > '" + stdin + "'\n"
	if err := os.WriteFile(op, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	w := NewCLIFallback(nil, &CLIBackend{opBin: op, vault: "V", prefix: "P"})
	if err := w.Write(context.Background(), "k", []byte("data")); err != nil {
		t.Fatal(err)
	}
	gotArgs, _ := os.ReadFile(args)
	gotIn, _ := os.ReadFile(stdin)
	if !strings.HasPrefix(string(gotArgs), "document edit P - k - --vault V") || string(gotIn) != "data" {
		t.Errorf("op args %q, stdin %q", gotArgs, gotIn)
	}
}
