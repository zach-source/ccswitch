package keychain

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
)

// ErrValueTooLarge means the value does not fit on one `security -i` line.
var ErrValueTooLarge = errors.New("value too large for security -i")

// AddGenericPassword stores data as a generic password through `bin -i` (the
// /usr/bin/security tool). The secret goes in hex on stdin, never in argv.
// env is added to the environment of bin; nil keeps the caller's. A non-empty
// keychain is the keychain file to write into. An existing item is updated.
func AddGenericPassword(bin string, env []string, service, account string, data []byte, keychain string) error {
	line := fmt.Sprintf("add-generic-password -U -s %q -a %q -X %s", service, account, hex.EncodeToString(data))
	if keychain != "" {
		line += fmt.Sprintf(" %q", keychain)
	}
	line += "\n"
	// security -i cuts each line at 4096 bytes and runs the first part, which
	// saves a cut-off value. Refuse a value that does not fit.
	// ponytail: 4000 leaves a margin under the 4096 cut.
	if len(line) > 4000 {
		return fmt.Errorf("the value is %d bytes, more than the keychain backend can write (about 1.9 KB): %w", len(data), ErrValueTooLarge)
	}
	return runSecurity(bin, env, line)
}

// hexRun matches a run of hex long enough to be part of a secret.
var hexRun = regexp.MustCompile(`[0-9a-fA-F]{16,}`)

// runSecurity feeds one command line to `bin -i`. security exits 0 even when
// the command fails, so any stderr output is the error.
func runSecurity(bin string, env []string, line string) error {
	cmd := exec.Command(bin, "-i")
	if env != nil {
		cmd.Env = append(cmd.Environ(), env...)
	}
	cmd.Stdin = strings.NewReader(line)
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	err := cmd.Run()
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		// security echoes a bad command line, which can hold the hex secret.
		return errors.New(hexRun.ReplaceAllString(msg, "<hex>"))
	}
	return err
}
