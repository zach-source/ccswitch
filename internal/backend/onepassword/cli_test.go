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
