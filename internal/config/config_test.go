package config

import (
	"net/netip"
	"strings"
	"testing"
)

func TestExampleConfigLoads(t *testing.T) {
	c, err := Load("../../config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if c.Location().String() != "Asia/Bangkok" || c.Defaults.IntervalMonths != 3 || len(c.SeedCalendars) == 0 {
		t.Errorf("config = %+v", c)
	}
	if len(c.Auth.Users) != 0 || c.Auth.AnonymousAccess != AnonymousNone || c.Auth.SessionHours != 12 {
		t.Errorf("auth = %+v", c.Auth)
	}
}

func TestValidation(t *testing.T) {
	_, err := Parse([]byte(`{
	  "holidays": ["13/04/2027"],
	  "auth": {"users": [{"username": "aa", "password": "x", "role": "boss"}, {"username": "aa", "password": "y"}]}
	}`))
	if err == nil {
		t.Fatal("want validation errors")
	}
	for _, want := range []string{`holiday "13/04/2027"`, "role must be", `duplicate user "aa"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
	if _, err := Parse([]byte(`{"typo_field": 1}`)); err == nil || !strings.Contains(err.Error(), "typo_field") {
		t.Errorf("unknown fields should be rejected: %v", err)
	}
	c, _ := Parse([]byte(`{"auth": {"users": [{"username": "viewer1", "password": "x"}]}}`))
	if c.Auth.Users[0].Role != RoleViewer || c.Auth.AnonymousAccess != AnonymousNone || c.Auth.SessionHours != 12 {
		t.Errorf("auth defaults = %+v", c.Auth)
	}
	if _, err := Parse([]byte(`{"auth": {"anonymous_access": "everyone", "users": [{"username": "Bad Name", "password": "x"}]}}`)); err == nil ||
		!strings.Contains(err.Error(), "anonymous_access") || !strings.Contains(err.Error(), "usernames are") {
		t.Errorf("want auth validation errors, got %v", err)
	}
	if RoleRank(RoleAdmin) <= RoleRank(RoleEditor) || RoleRank(RoleEditor) <= RoleRank(RoleViewer) {
		t.Error("role order")
	}
}

func TestProxiesAndSecrets(t *testing.T) {
	c, err := Parse([]byte(`{"trusted_proxies": ["172.16.0.0/12", "10.0.0.5"], "public_url": "https://pm.example.com", "feed": {"token": "replace-with-a-long-random-string"}}`))
	if err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[string]bool{"172.18.0.2": true, "10.0.0.5": true, "10.0.0.6": false, "127.0.0.1": false, "203.0.113.9": false} {
		if got := c.TrustedProxy(netip.MustParseAddr(addr)); got != want {
			t.Errorf("TrustedProxy(%s) = %v", addr, got)
		}
	}
	if !c.HTTPS() || len(c.SecretProblems()) != 1 {
		t.Errorf("https=%v problems=%v", c.HTTPS(), c.SecretProblems())
	}
	d, _ := Parse([]byte(`{}`))
	if !d.TrustedProxy(netip.MustParseAddr("127.0.0.1")) || !d.TrustedProxy(netip.MustParseAddr("::1")) || d.TrustedProxy(netip.MustParseAddr("192.168.1.10")) {
		t.Error("default trusted proxies are loopback only")
	}
	if _, err := Parse([]byte(`{"trusted_proxies": ["nope"], "public_url": "pm.example.com"}`)); err == nil ||
		!strings.Contains(err.Error(), "trusted_proxies") || !strings.Contains(err.Error(), "public_url") {
		t.Errorf("want proxy and url errors, got %v", err)
	}
}
