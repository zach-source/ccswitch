package cli

import (
	"strings"
	"testing"
)

func TestGrokWarnsWhenAPIKeyIsSet(t *testing.T) {
	t.Setenv("XAI_API_KEY", "")
	if w := grokSpec.Warn(); w != "" {
		t.Fatalf("warning without a key: %q", w)
	}
	t.Setenv("XAI_API_KEY", "xai-test")
	if w := grokSpec.Warn(); !strings.Contains(w, "XAI_API_KEY") || strings.Contains(w, "xai-test") {
		t.Fatalf("warning = %q", w)
	}
}

func TestGrokIsRegistered(t *testing.T) {
	p := grokProvider()
	if p.Name() != "grok" || p.CredKeyPrefix() != "Grok-Account-" || !strings.HasSuffix(p.RegistryPath(), "grok.json") {
		t.Fatalf("provider = %s %s %s", p.Name(), p.CredKeyPrefix(), p.RegistryPath())
	}
}
