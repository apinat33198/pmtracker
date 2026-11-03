package config

import (
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
