// Package checks probes public URLs and the certificates nginx serves.
package checks

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/olayanju-1234/vps-dashboard/internal/state"
)

// HTTP checks URLs with a short timeout and a recognisable User-Agent, so the
// nginx counter can leave these requests out.
type HTTP struct {
	Client    *http.Client
	UserAgent string
}

// NewHTTP returns a checker that does not follow redirects: a 301 to a login
// page should not look like a healthy 200.
func NewHTTP(ua string) *HTTP {
	return &HTTP{UserAgent: ua, Client: &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// Check GETs one URL.
func (h *HTTP) Check(ctx context.Context, url string, expect int) state.Check {
	c := state.Check{URL: url, Expect: expect, CheckedAt: time.Now().UTC()}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		c.Error = err.Error()
		return c
	}
	req.Header.Set("User-Agent", h.UserAgent)
	start := time.Now()
	resp, err := h.Client.Do(req)
	c.LatencyMS = float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		c.Error = err.Error()
		return c
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	c.Status = resp.StatusCode
	c.OK = resp.StatusCode == expect
	return c
}

// Cert dials addr with SNI host and reads the certificate nginx picks for it.
// Verification runs separately so an expired or wrong cert still reports dates.
func Cert(ctx context.Context, addr, host string, roots *x509.CertPool, now time.Time) state.Cert {
	c := state.Cert{Host: host}
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true, // verified below, against the same name
		MinVersion:         tls.VersionTLS12,
	}}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		c.Error = fmt.Sprintf("dial: %v", err)
		return c
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		c.Error = "no certificate"
		return c
	}
	leaf := certs[0]
	sum := sha256.Sum256(leaf.Raw)
	c.Subject = leaf.Subject.CommonName
	c.Issuer = leaf.Issuer.CommonName
	if c.Issuer == "" && len(leaf.Issuer.Organization) > 0 {
		c.Issuer = leaf.Issuer.Organization[0]
	}
	c.Names = leaf.DNSNames
	c.NotAfter = leaf.NotAfter.UTC()
	c.DaysLeft = float64(int(leaf.NotAfter.Sub(now).Hours()/24*10)) / 10
	c.Fingerprint = hex.EncodeToString(sum[:8])
	inter := x509.NewCertPool()
	for _, ic := range certs[1:] {
		inter.AddCert(ic)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: roots, Intermediates: inter, CurrentTime: now}); err != nil {
		c.Error = err.Error()
	} else {
		c.Valid = true
	}
	return c
}
