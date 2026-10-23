package checks

import (
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPCheck(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte("ok"))
		case "/moved":
			http.Redirect(w, r, "/ok", http.StatusMovedPermanently)
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	h := NewHTTP("vps-dashboard-test")
	ctx := context.Background()
	if c := h.Check(ctx, srv.URL+"/ok", 200); !c.OK || c.Status != 200 || c.LatencyMS <= 0 || ua != "vps-dashboard-test" {
		t.Fatalf("ok: %+v ua=%q", c, ua)
	}
	if c := h.Check(ctx, srv.URL+"/moved", 200); c.OK || c.Status != 301 {
		t.Fatalf("redirect must not be followed: %+v", c)
	}
	if c := h.Check(ctx, srv.URL+"/moved", 301); !c.OK {
		t.Fatalf("expected 301: %+v", c)
	}
	if c := h.Check(ctx, srv.URL+"/down", 200); c.OK || c.Status != 502 {
		t.Fatalf("502: %+v", c)
	}
	if c := h.Check(ctx, "http://127.0.0.1:1/", 200); c.OK || c.Error == "" {
		t.Fatalf("refused: %+v", c)
	}
}

func TestCert(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "https://")
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	now := time.Now()
	c := Cert(context.Background(), addr, "example.com", roots, now)
	if !c.Valid || c.Error != "" || c.NotAfter.IsZero() || c.DaysLeft <= 0 || c.Fingerprint == "" {
		t.Fatalf("valid cert: %+v", c)
	}
	if c := Cert(context.Background(), addr, "wrong.example.org", roots, now); c.Valid || c.Error == "" || c.NotAfter.IsZero() {
		t.Fatalf("name mismatch should report dates but not be valid: %+v", c)
	}
	if c := Cert(context.Background(), addr, "example.com", x509.NewCertPool(), now); c.Valid {
		t.Fatalf("unknown root must not verify: %+v", c)
	}
}
