package notify

import (
	"testing"
)

func TestEscapeQuotes(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`hello world`, `hello world`},
		{`hello "world"`, `hello \"world\"`},
		{`back\slash`, `back\\slash`},
	}
	for _, c := range cases {
		if got := escapeQuotes(c.in); got != c.want {
			t.Errorf("escapeQuotes(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSendNonBlocking(t *testing.T) {
	// Must not panic or crash
	Send("test", "message")
	Send("", "message")
}
