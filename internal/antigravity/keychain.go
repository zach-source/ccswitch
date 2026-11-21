package antigravity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/zach-source/ccswitch/internal/backend/keychain"
	"github.com/zach-source/ccswitch/internal/provider"
)

// agy keeps its login in one macOS keychain item, written with go-keyring.
const (
	keychainService = "gemini"
	keychainAccount = "antigravity"
)

// Tool paths. Tests point them at fake scripts.
var (
	securityBin = "/usr/bin/security"
	agyBin      = "agy"
)

// readItem reads the agy item from the keychain that env selects. security
// resolves the keychain from $HOME. Exit 44 means no item.
func readItem(ctx context.Context, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, securityBin, "find-generic-password", "-s", keychainService, "-a", keychainAccount, "-w")
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 44 {
			return nil, provider.ErrNoLiveLogin
		}
		return nil, fmt.Errorf("read the agy keychain item: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	// security -w ends the value with one newline.
	return bytes.TrimSuffix(stdout.Bytes(), []byte("\n")), nil
}

func security(ctx context.Context, env []string, args ...string) error {
	cmd := exec.CommandContext(ctx, securityBin, args...)
	cmd.Env = append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("security %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

// withIsolatedHome runs fn in a temporary home with its own empty login
// keychain, so agy there cannot touch the real login. When blob is not nil it
// is put into that keychain first. After fn it reads the item back and
// returns it when it differs from blob: agy signed in or refreshed it. The
// keychain and the home are always removed.
func withIsolatedHome(ctx context.Context, blob []byte, fn func(home string) error) (changed []byte, err error) {
	home, err := os.MkdirTemp("", "ccswitch-agy-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(home)
	env := []string{"HOME=" + home}
	kc := filepath.Join(home, "Library", "Keychains", "login.keychain-db")
	if err := os.MkdirAll(filepath.Dir(kc), 0o700); err != nil {
		return nil, err
	}
	if err := security(ctx, env, "create-keychain", "-p", "", kc); err != nil {
		return nil, err
	}
	// Clean up even when ctx is done.
	defer func() { _ = security(context.Background(), env, "delete-keychain", kc) }()
	for _, args := range [][]string{
		{"default-keychain", "-d", "user", "-s", kc},
		{"list-keychains", "-d", "user", "-s", kc},
		{"unlock-keychain", "-p", "", kc},
		{"set-keychain-settings", kc},
	} {
		if err := security(ctx, env, args...); err != nil {
			return nil, err
		}
	}
	if blob != nil {
		if err := keychain.AddGenericPassword(securityBin, env, keychainService, keychainAccount, blob, kc); err != nil {
			return nil, fmt.Errorf("copy the login into the isolated keychain: %w", err)
		}
	}
	fnErr := fn(home)
	after, rerr := readItem(context.Background(), env)
	if rerr == nil && !bytes.Equal(after, blob) {
		changed = after
	}
	return changed, fnErr
}
