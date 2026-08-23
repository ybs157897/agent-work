package kimiapp

import "testing"

func TestSanitizeKapLogLine(t *testing.T) {
	in := "  Local:    http://127.0.0.1:58337/#token=secret-token-value"
	got := sanitizeKapLogLine(in)
	if got != "  Local:    [kap-server]" {
		t.Fatalf("got %q", got)
	}
	if sanitizeKapLogLine("  Token:    abc123") != "  Token: [redacted]" {
		t.Fatal("token line not redacted")
	}
}
