package codex_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zach-source/ccswitch/internal/codex"
)

func regWith(priority []string, labels ...string) *codex.Registry {
	r := &codex.Registry{Version: codex.SchemaVersion, Priority: priority, Profiles: map[string]codex.Profile{}}
	for _, l := range labels {
		r.Profiles[l] = codex.Profile{Label: l}
	}
	return r
}

func TestOrdered(t *testing.T) {
	tests := []struct {
		name     string
		priority []string
		labels   []string
		want     []string
	}{
		{"priority order wins", []string{"c", "a", "b"}, []string{"a", "b", "c"}, []string{"c", "a", "b"}},
		{"unknown and duplicate labels skipped", []string{"b", "ghost", "b", "a"}, []string{"a", "b"}, []string{"b", "a"}},
		{"labels missing from priority come after, sorted", []string{"c"}, []string{"d", "a", "c", "b"}, []string{"c", "a", "b", "d"}},
		{"empty priority equals Labels", nil, []string{"b", "c", "a"}, []string{"a", "b", "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := regWith(tt.priority, tt.labels...)
			if got := r.Ordered(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Ordered() = %v, want %v", got, tt.want)
			}
		})
	}
	// promax always comes before pro, whatever the priority says. Plans of the
	// same rank keep the priority order.
	r := regWith([]string{"pro1", "plus", "max", "pro2"}, "pro1", "plus", "max", "pro2")
	for l, plan := range map[string]string{"pro1": "pro", "plus": "plus", "max": "promax", "pro2": "pro"} {
		r.Profiles[l] = codex.Profile{Label: l, Plan: plan}
	}
	if got, want := r.Ordered(), []string{"max", "pro1", "pro2", "plus"}; !reflect.DeepEqual(got, want) {
		t.Errorf("plan rank: Ordered() = %v, want %v", got, want)
	}
	r = regWith(nil, "b", "a")
	if got, want := r.Ordered(), r.Labels(); !reflect.DeepEqual(got, want) {
		t.Errorf("Ordered() = %v, Labels() = %v", got, want)
	}
}

func TestRegistrySaveLoadRoundTripsPriorityAndLastSwitch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex.json")
	r := regWith([]string{"b", "a"}, "a", "b")
	r.LastSwitch = time.Date(2026, 10, 17, 1, 20, 13, 376697000, time.UTC)
	if err := r.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := codex.LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if !reflect.DeepEqual(got.Priority, r.Priority) {
		t.Errorf("Priority = %v, want %v", got.Priority, r.Priority)
	}
	if !got.LastSwitch.Equal(r.LastSwitch) {
		t.Errorf("LastSwitch = %v, want %v", got.LastSwitch, r.LastSwitch)
	}
}

func TestLoadRegistryAcceptsFileWithoutNewFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex.json")
	old := `{"version":1,"active":"a","profiles":{"a":{"label":"a"}}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := codex.LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if len(got.Priority) != 0 || !got.LastSwitch.IsZero() || got.Active != "a" {
		t.Errorf("unexpected registry: %+v", got)
	}
}

func TestRegistryOmitsEmptyNewFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex.json")
	if err := regWith(nil, "a").Save(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	for _, key := range []string{"priority", "lastSwitch"} {
		if strings.Contains(string(data), key) {
			t.Errorf("saved file contains %q: %s", key, data)
		}
	}
}
