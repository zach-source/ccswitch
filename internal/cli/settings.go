package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// settingsPath returns the path to ~/.claude/settings.json — the file the
// API-switching commands (use-zai / use-anthropic / api-status) read and
// write. This is distinct from claudeConfigPath() (~/.claude/.claude.json).
func settingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

// readSettings loads settings.json into a generic map. A missing file is
// not an error — it returns an empty map and ok=false so callers can tell
// "no file" apart from "empty file".
func readSettings(path string) (settings map[string]any, ok bool, err error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read settings: %w", err)
	}
	m := map[string]any{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, false, fmt.Errorf("parse settings %s: %w", path, err)
		}
	}
	return m, true, nil
}

// writeSettings serializes m to path atomically (write-temp + rename) with
// 0600 perms, creating the parent directory if needed. It marshals first so
// a marshal failure never truncates the existing file.
//
// The temp file gets a randomized name from os.CreateTemp rather than a
// predictable "<path>.tmp": settings.json can carry a secret (the z.ai
// ANTHROPIC_AUTH_TOKEN written by use-zai), and a fixed temp name could be
// pre-created as a symlink to redirect that secret-bearing write, or be left
// behind containing the token if the process dies mid-write.
func writeSettings(path string, m map[string]any) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".settings-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	committed = true
	return nil
}
