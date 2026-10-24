package cli

import "testing"

const testToken = "sk-ant-oat01-AbCdEf0123456789_-AbCdEf0123456789_-AbCdEf0123456789_-AbCdEf0123456789_-AbCdEf0123456789_-AAAA"

func TestParseSetupToken(t *testing.T) {
	cut := 60
	tests := []struct {
		name, in, want string
	}{
		{
			"plain",
			"Your OAuth token (valid for 1 year):\n\n" + testToken + "\n\nStore this token securely.\n",
			testToken,
		},
		{
			"ansi and carriage returns",
			"\x1b[2K\x1b[1A\x1b[32m✓\x1b[39m Long-lived token created\r\n\r\n\x1b[1m" + testToken + "\x1b[22m\r\n\r\nStore it\r\n",
			testToken,
		},
		{
			"wrapped across lines",
			"token:\r\n\r\n" + testToken[:cut] + "\r\n" + testToken[cut:] + "\r\n\r\nStore this token securely.\r\n",
			testToken,
		},
		{
			"wrapped with ansi at the break",
			testToken[:cut] + "\x1b[0m\r\n\x1b[1m" + testToken[cut:] + "\r\n\r\nUse it as CLAUDE_CODE_OAUTH_TOKEN\r\n",
			testToken,
		},
		{
			"redrawn twice keeps the longest",
			"sk-ant-oat01-short\r\n" + testToken + "\r\n",
			testToken,
		},
		{"none", "Browser didn't open? Use the url below\r\nhttps://claude.ai/oauth\r\n", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseSetupToken(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
