package browser

import (
	"os"
	"path/filepath"
)

// noOpenerScript always fails, so whatever invoked it falls through to its
// own "couldn't open a browser" text fallback instead of a GUI ever
// appearing. It never touches the real system opener at all.
const noOpenerScript = "#!/usr/bin/env bash\n" +
	"# ccswitch no-browser shim: deliberately fails so the caller (claude)\n" +
	"# falls back to printing the OAuth URL as text instead of launching a\n" +
	"# local browser.\n" +
	"exit 1\n"

// InstallNoOpener writes `open`/`xdg-open` shims at dir that always fail,
// guaranteeing an OAuth login flow that shells out to the system opener
// falls through to its own manual/headless fallback (print the URL, wait
// for a pasted response) instead of launching a real local browser.
//
// Unlike InstallOpener, this does not require a browser to be installed at
// all — it exists purely to suppress browser launching for the headless
// login path, on any OS.
func InstallNoOpener(dir string) error {
	_, names, err := openerNames()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(noOpenerScript), 0o755); err != nil {
			return err
		}
	}
	return nil
}
