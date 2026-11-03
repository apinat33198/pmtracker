package auth

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestHashAndVerify(t *testing.T) {
	Iterations = 1000
	h, err := Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !IsHash(h) || !strings.HasPrefix(h, "pbkdf2-sha256$1000$") {
		t.Errorf("hash = %s", h)
	}
	h2, _ := Hash("correct horse battery")
	if h == h2 {
		t.Error("salts should differ")
	}
	if !Verify("correct horse battery", h) || Verify("correct horse batterY", h) || Verify("", h) {
		t.Error("verify")
	}
	if !Verify("plain-pw", "plain-pw") || Verify("x", "plain-pw") || Verify("", "") {
		t.Error("plain-text config passwords")
	}
	for _, bad := range []string{"pbkdf2-sha256$", "pbkdf2-sha256$x$a$b", "pbkdf2-sha256$10$!!$!!"} {
		if Verify("x", bad) {
			t.Errorf("malformed hash %q verified", bad)
		}
	}
}

func TestPasswordRules(t *testing.T) {
	if CheckPassword("short", "a") == "" || CheckPassword("facilities", "facilities") == "" || CheckPassword("a long enough one", "bob") != "" {
		t.Error("password rules")
	}
	for u, ok := range map[string]bool{"admin": true, "nok.s": true, "a": false, "Admin": false, "has space": false, "x|y": false} {
		if ValidUsername(u) != ok {
			t.Errorf("ValidUsername(%q) = %v", u, !ok)
		}
	}
}

func TestSessions(t *testing.T) {
	secret := NewSecret()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	v := Sign(secret, Session{Username: "admin", Epoch: 2, Expires: now.Add(time.Hour)})
	s, err := Parse(secret, v, now)
	if err != nil || s.Username != "admin" || s.Epoch != 2 {
		t.Fatalf("parse = %+v, %v", s, err)
	}
	if _, err := Parse(secret, v, now.Add(2*time.Hour)); err == nil {
		t.Error("expired session accepted")
	}
	if _, err := Parse(NewSecret(), v, now); err == nil {
		t.Error("session signed with another secret accepted")
	}
	tampered := strings.Replace(v, v[:4], "AAAA", 1)
	if _, err := Parse(secret, tampered, now); err == nil {
		t.Error("tampered session accepted")
	}
	if _, err := Parse(secret, "garbage", now); err == nil {
		t.Error("garbage accepted")
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		if l.Fail("admin", "10.0.0.1", now.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("locked too early at %d", i)
		}
	}
	if !l.Fail("admin", "10.0.0.1", now.Add(5*time.Second)) {
		t.Fatal("fifth failure should lock")
	}
	if l.Blocked("admin", "10.0.0.1", now.Add(time.Minute)) == 0 {
		t.Error("should be blocked")
	}
	if l.Blocked("other", "10.0.0.1", now.Add(time.Minute)) != 0 {
		t.Error("other usernames from the address are not blocked yet")
	}
	if l.Blocked("admin", "10.0.0.1", now.Add(16*time.Minute)) != 0 {
		t.Error("lockout should expire")
	}
	// Old failures fall out of the window.
	l2 := NewLimiter()
	for i := 0; i < 4; i++ {
		l2.Fail("bob", "a", now)
	}
	if l2.Fail("bob", "a", now.Add(20*time.Minute)) {
		t.Error("failures outside the window shouldn't count")
	}
	// Many usernames from one address.
	l3 := NewLimiter()
	locked := false
	for i := 0; i < 20; i++ {
		locked = l3.Fail(strings.Repeat("u", i%4+2), "evil", now) || locked
	}
	if l3.Blocked("someone-new", "evil", now) == 0 {
		t.Error("address-wide lockout after 20 failures")
	}
}

func TestLimiterStaysBounded(t *testing.T) {
	l := NewLimiter()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3*maxEntries; i++ {
		l.Fail("u", fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255), now.Add(time.Duration(i)*time.Millisecond))
	}
	if n := len(l.failures) + len(l.locked); n > maxEntries+2 {
		t.Errorf("limiter holds %d entries", n)
	}
}

func TestLimiterCapsGuessesFromFakedAddresses(t *testing.T) {
	l := NewLimiter()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		l.Fail("admin", fmt.Sprintf("10.9.0.%d", i), now)
	}
	if l.Blocked("admin", "10.9.1.1", now) == 0 {
		t.Error("30 failures for one username from any addresses should block it")
	}
	if l.Blocked("someone", "10.9.1.1", now) != 0 {
		t.Error("other usernames aren't affected")
	}
}
