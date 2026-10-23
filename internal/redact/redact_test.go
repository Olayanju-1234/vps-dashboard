package redact

import (
	"strings"
	"testing"
)

func TestLine(t *testing.T) {
	cases := []struct {
		name, in, secret, keep string
	}{
		{"url creds", "connect postgres://shop:Sup3rS3cret@127.0.0.1:5432/shop failed", "Sup3rS3cret", "127.0.0.1:5432/shop"},
		{"amqp creds", "amqp://shop:hunter2hunter2@localhost:5672/shop", "hunter2hunter2", "localhost:5672"},
		{"jwt", `"authorization":"x eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.abcDEF123_-xyz"`, "eyJzdWIiOiIxMjMifQ", "authorization"},
		{"bearer", "Authorization: Bearer abcdef0123456789abcdef", "abcdef0123456789abcdef", "Authorization"},
		{"stripe", "using key sk_test_51HabcdefGHIJKLmnop", "sk_test_51HabcdefGHIJKLmnop", "using key"},
		{"lk_ key", "api key lk_live_AbCdEf123456 rejected", "lk_live_AbCdEf123456", "rejected"},
		{"resend", "resend re_AbCdEfGh12345678ijkl", "re_AbCdEfGh12345678ijkl", "resend"},
		{"github", "ghp_abcdefghijklmnopqrstuvwxyz0123", "ghp_abcdefghijklmnopqrstuvwxyz0123", ""},
		{"kv secret", `password=pa55word123 user=bob`, "pa55word123", "user=bob"},
		{"json token", `{"x-internal-token":"tok-abc-123-xyz","path":"/health"}`, "tok-abc-123-xyz", `"path":"/health"`},
		{"api_key", `API_KEY: abcd1234efgh`, "abcd1234efgh", "API_KEY"},
		{"cookie", `cookie=sid%3Dabcdef1234`, "sid%3Dabcdef1234", ""},
		{"long hex", "hmac 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08 ok", "9f86d081884c7d659a2feaa0c55ad015", "ok"},
		{"long b64", "blob QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVphYmNkZWZnaGlqa2xtbm9w end", "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVph", "end"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Line(c.in)
			if strings.Contains(got, c.secret) {
				t.Fatalf("secret survived: %q -> %q", c.in, got)
			}
			if c.keep != "" && !strings.Contains(got, c.keep) {
				t.Fatalf("over-redacted, lost %q: %q", c.keep, got)
			}
		})
	}
}

func TestLineLeavesOrdinaryLogsAlone(t *testing.T) {
	for _, s := range []string{
		`level=ERROR msg="append failed" workflow_id=order-42 step=charge`,
		`[Nest] 1234  - 10/23/2026, 7:12:40 AM   ERROR [TenantService] Tenant acme failed at step dns`,
		`shop-api.service: Main process exited, code=exited, status=1/FAILURE`,
		`GET /api/workflows?limit=5 200 12ms`,
	} {
		if got := Line(s); got != s {
			t.Fatalf("changed an ordinary line:\n in %q\nout %q", s, got)
		}
	}
}
