package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/olayanju-1234/vps-dashboard/internal/demo"
)

func newTest(t *testing.T) *httptest.Server {
	assets := fstest.MapFS{"index.html": {Data: []byte("<!doctype html>hi")}}
	srv := httptest.NewServer(New(demo.New("t"), assets, "t", slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, method, url, host string) (*http.Response, string) {
	req, _ := http.NewRequestWithContext(context.Background(), method, url, nil)
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func TestRoutes(t *testing.T) {
	srv := newTest(t)
	for _, p := range []string{"/api/state", "/api/history?range=6h", "/api/journal?project=billing", "/healthz", "/"} {
		resp, body := do(t, http.MethodGet, srv.URL+p, "")
		if resp.StatusCode != 200 || body == "" {
			t.Fatalf("GET %s: %d", p, resp.StatusCode)
		}
		if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || resp.Header.Get("X-Frame-Options") != "DENY" {
			t.Fatalf("security headers missing on %s", p)
		}
	}
	if resp, _ := do(t, http.MethodGet, srv.URL+"/api/history?range=forever", ""); resp.StatusCode != 400 {
		t.Fatalf("bad range: %d", resp.StatusCode)
	}
	if resp, _ := do(t, http.MethodGet, srv.URL+"/api/journal?project=nope", ""); resp.StatusCode != 404 {
		t.Fatalf("unknown project: %d", resp.StatusCode)
	}
}

func TestReadOnly(t *testing.T) {
	srv := newTest(t)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if resp, _ := do(t, m, srv.URL+"/api/state", ""); resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s: %d", m, resp.StatusCode)
		}
	}
}

func TestHostHeaderGuard(t *testing.T) {
	srv := newTest(t)
	for host, want := range map[string]int{
		"localhost:8400": 200, "127.0.0.1:8400": 200, "[::1]:8400": 200, "localhost": 200,
		"evil.example.com": http.StatusMisdirectedRequest, "evil.example.com:8400": http.StatusMisdirectedRequest, "10.0.0.5:8400": http.StatusMisdirectedRequest,
	} {
		if resp, _ := do(t, http.MethodGet, srv.URL+"/api/state", host); resp.StatusCode != want {
			t.Errorf("Host %q: got %d want %d", host, resp.StatusCode, want)
		}
	}
}
