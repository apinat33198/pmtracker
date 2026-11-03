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
	if len(c.Auth.Users) == 0 || c.Auth.Users[0].Role != RoleAdmin {
		t.Errorf("users = %+v", c.Auth.Users)
	}
}

func TestValidation(t *testing.T) {
	_, err := Parse([]byte(`{
	  "holidays": ["13/04/2027"],
	  "auth": {"users": [{"username": "a", "password": "x", "role": "boss"}, {"username": "a", "password": "y"}]}
	}`))
	if err == nil {
		t.Fatal("want validation errors")
	}
	for _, want := range []string{`holiday "13/04/2027"`, "role must be", `duplicate user "a"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
	if _, err := Parse([]byte(`{"typo_field": 1}`)); err == nil || !strings.Contains(err.Error(), "typo_field") {
		t.Errorf("unknown fields should be rejected: %v", err)
	}
	c, _ := Parse([]byte(`{"auth": {"users": [{"username": "v", "password": "x"}]}}`))
	if c.Auth.Users[0].Role != RoleViewer {
		t.Error("role defaults to viewer")
	}
	if RoleRank(RoleAdmin) <= RoleRank(RoleEditor) || RoleRank(RoleEditor) <= RoleRank(RoleViewer) {
		t.Error("role order")
	}
}
