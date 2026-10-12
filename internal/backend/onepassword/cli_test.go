package onepassword

import "testing"

func TestCLIBackend_AccountShorthand(t *testing.T) {
	cases := map[string]string{
		"stigenai.1password.com":         "stigenai",
		"https://stigenai.1password.com": "stigenai",
		"http://my.1password.com":        "my",
		"my.1password.com":               "my",
		"single":                         "single",
		"":                               "",
	}
	for in, want := range cases {
		b := &CLIBackend{account: in}
		if got := b.accountShorthand(); got != want {
			t.Errorf("accountShorthand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWithoutEnv(t *testing.T) {
	got := withoutEnv([]string{"A=1", "OP_SERVICE_ACCOUNT_TOKEN=x", "OP_SERVICE_ACCOUNT_TOKEN_X=y", "B="}, "OP_SERVICE_ACCOUNT_TOKEN")
	want := []string{"A=1", "OP_SERVICE_ACCOUNT_TOKEN_X=y", "B="}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
