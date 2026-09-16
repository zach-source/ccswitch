package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// The printed command is the deliverable of this subcommand — if it is wrong,
// the user pastes a broken grant into a privileged shell.
func TestServiceAccountCommand(t *testing.T) {
	cases := []struct {
		name                            string
		sa, vault, expiresIn, opAccount string
		want                            []string
		reject                          []string
	}{
		{
			name: "bare words stay unquoted",
			sa:   "ccswitch-laptop", vault: "ccswitch", expiresIn: "90d",
			want: []string{
				"op service-account create ccswitch-laptop",
				"--vault ccswitch:read_items,write_items",
				"--expires-in 90d",
			},
		},
		{
			name: "a vault name with a space survives copy-paste",
			sa:   "ccswitch", vault: "Claude Agents", expiresIn: "",
			want:   []string{`--vault "Claude Agents:read_items,write_items"`},
			reject: []string{"--expires-in"},
		},
		{
			name: "account shorthand is carried through",
			sa:   "ccswitch", vault: "ccswitch", expiresIn: "24h", opAccount: "stigenai.1password.com",
			want: []string{"op --account stigenai.1password.com service-account create"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := serviceAccountCommand(tc.sa, tc.vault, tc.expiresIn, tc.opAccount)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("command missing %q:\n%s", w, got)
				}
			}
			for _, r := range tc.reject {
				if strings.Contains(got, r) {
					t.Errorf("command should not contain %q:\n%s", r, got)
				}
			}
		})
	}
}

// ccswitch writes credentials, so a read-only grant would half-work: reads
// succeed, every refresh and push fails. And it never shares items.
func TestServiceAccountGrantIsReadWriteAndNothingMore(t *testing.T) {
	if opVaultPermissions != "read_items,write_items" {
		t.Fatalf("grant = %q; ccswitch reads and writes, and must not request share_items", opVaultPermissions)
	}
}

// 1Password forbids granting a service account the Private/Personal vault,
// so the command must never steer a user into configuring one.
func TestReservedVaultsAreRejected(t *testing.T) {
	for _, name := range []string{"Private", "private", "Personal", " personal ", "PERSONAL"} {
		if !isReservedOpVault(name) {
			t.Errorf("isReservedOpVault(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"ccswitch", "Claude Agents", "privateer", ""} {
		if isReservedOpVault(name) {
			t.Errorf("isReservedOpVault(%q) = true, want false", name)
		}
	}
}

func TestGuideCoversTheHeadlessPath(t *testing.T) {
	var buf bytes.Buffer
	writeServiceAccountGuide(&buf, "ccswitch", "ccswitch-laptop", "90d", "")
	got := buf.String()

	for _, want := range []string{
		"OP_SERVICE_ACCOUNT_TOKEN", // the variable that makes it headless
		"once",                     // the token is shown exactly once
		"Private or Personal",      // why a dedicated vault is required
		"Business or Teams",        // the plan requirement
		"ccswitch push",            // populating the new vault
		"launchd",                  // the daemon this user actually runs
	} {
		if !strings.Contains(got, want) {
			t.Errorf("guide never mentions %q:\n%s", want, got)
		}
	}

	// A real token must never be baked into printed instructions.
	if strings.Contains(got, "ops_") && !strings.Contains(got, "ops_…") {
		t.Errorf("guide appears to contain a literal token:\n%s", got)
	}
}

func TestPatchTOMLOpVault_SetsVaultAndKeepsSiblings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	seed := "[backend]\ntype = \"1password\"\n\n[backend.onepassword]\nconnect_host = \"https://op.example.com\"\nitem_prefix = \"Claude Code Account\"\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := patchTOMLOpVault(path, "ccswitch"); err != nil {
		t.Fatalf("patchTOMLOpVault: %v", err)
	}

	var cfg map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	op, _ := cfg["backend"].(map[string]any)["onepassword"].(map[string]any)
	if op["vault"] != "ccswitch" {
		t.Errorf("vault = %v, want ccswitch", op["vault"])
	}
	// Rewriting the file must not drop unrelated settings.
	if op["connect_host"] != "https://op.example.com" {
		t.Errorf("connect_host was lost: %v", op["connect_host"])
	}
	if op["item_prefix"] != "Claude Code Account" {
		t.Errorf("item_prefix was lost: %v", op["item_prefix"])
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("config.toml mode = %o, want 600", mode)
	}
}

func TestPatchTOMLOpVault_CreatesFileWhenAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	if err := patchTOMLOpVault(path, "ccswitch"); err != nil {
		t.Fatalf("patchTOMLOpVault: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config was not created: %v", err)
	}
	if !strings.Contains(string(data), "ccswitch") {
		t.Errorf("config does not name the vault:\n%s", data)
	}
}

// --print-only is the flag for scripting and for reading the instructions on
// a machine that is not signed in; it must not shell out to op at all.
func TestSetupOpVaultPrintOnly_TouchesNothing(t *testing.T) {
	home := newTestHome(t)
	if err := run(t, "setup-op-vault", "--print-only", "--vault", "ccswitch"); err != nil {
		t.Fatalf("setup-op-vault --print-only: %v", err)
	}
	cfgPath := filepath.Join(home, ".config", "ccswitch", "config.toml")
	if _, err := os.Stat(cfgPath); err == nil {
		t.Error("--print-only wrote the config file")
	}
}

func TestDefaultServiceAccountNameIsHostScoped(t *testing.T) {
	got := defaultServiceAccountName()
	if !strings.HasPrefix(got, "ccswitch") {
		t.Errorf("name = %q, want a ccswitch- prefix so the grant is identifiable", got)
	}
	if strings.HasSuffix(got, ".local") {
		t.Errorf("name = %q, should drop the mDNS suffix", got)
	}
}
