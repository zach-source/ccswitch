package onepassword

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsPermissionDenied(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"403", &httpError{status: 403}, true},
		{"wrapped 403", fmt.Errorf("update %q: %w", "acct", &httpError{status: 403}), true},
		{"404", &httpError{status: 404}, false},
		{"500", &httpError{status: 500}, false},
		{"other error", errors.New("boom"), false},
	}
	for _, c := range cases {
		if got := isPermissionDenied(c.err); got != c.want {
			t.Errorf("%s: isPermissionDenied = %v, want %v", c.name, got, c.want)
		}
	}
}
