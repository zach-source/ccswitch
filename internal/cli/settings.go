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
func writeSettings(path string, m map[string]any) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
