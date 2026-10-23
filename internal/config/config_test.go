package config

import (
	"strings"
	"testing"
	"time"
)

func TestParseDefaults(t *testing.T) {
	c, err := Parse([]byte(`{"projects":[{"id":"shop","checks":[{"url":"https://x"}],"backups":[{"dir":"/b"}],"env_files":[{"path":"/e"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	p := c.Projects[0]
	if c.Listen != "127.0.0.1:8400" || p.Name != "shop" || p.Checks[0].Expect != 200 || p.Backups[0].MaxAge.Duration != 26*time.Hour ||
		p.EnvFiles[0].ExpectMode != "0600" || p.Deploy.Method != "none" || c.Intervals.Probe.Duration != time.Minute {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"public listen":   `{"listen":"0.0.0.0:8400"}`,
		"unknown field":   `{"lisen":"127.0.0.1:1"}`,
		"duplicate id":    `{"projects":[{"id":"a"},{"id":"a"}]}`,
		"port clash":      `{"ports":[{"port":22,"project":"x"}],"projects":[{"id":"a","ports":[{"port":22}]}]}`,
		"bad deploy":      `{"projects":[{"id":"a","deploy":{"method":"ftp"}}]}`,
		"tree needs repo": `{"projects":[{"id":"a","deploy":{"method":"tree","trees":[{"dir":"/x"}]}}]}`,
		"bad duration":    `{"intervals":{"fast":"soon"}}`,
		"missing id":      `{"projects":[{"name":"x"}]}`,
		"bad log_ignore":  `{"projects":[{"id":"a","log_ignore":["("]}]}`,
	}
	for name, js := range cases {
		if _, err := Parse([]byte(js)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := Parse([]byte(`{"listen":"[::1]:9000"}`)); err != nil {
		t.Errorf("ipv6 loopback should be fine: %v", err)
	}
}

func TestProjectFor(t *testing.T) {
	c, _ := Parse([]byte(`{"projects":[{"id":"a","units":["web"]}]}`))
	if c.ProjectFor("web.service") != "a" || c.ProjectFor("other") != "" || !strings.HasSuffix(c.Listen, c.ListenPort()) {
		t.Fatal("ProjectFor/ListenPort")
	}
}
