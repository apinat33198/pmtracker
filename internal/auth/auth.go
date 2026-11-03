// Package auth handles accounts: password hashing, signed session cookies
// and the failed sign-in limiter. Standard library only.
package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// User is an account stored in the data file.
type User struct {
	Username     string    `json:"username"`
	Role         string    `json:"role"`
	PasswordHash string    `json:"password_hash"`
	Epoch        int       `json:"epoch"` // bumped to sign the user out everywhere
	Created      time.Time `json:"created"`
	LastLogin    time.Time `json:"last_login,omitempty"`
}

// Iterations for new hashes (OWASP 2023 guidance for PBKDF2-HMAC-SHA256).
// Tests lower it to stay fast.
var Iterations = 600_000

const hashPrefix = "pbkdf2-sha256$"

// MinPasswordLength for passwords set through the app.
const MinPasswordLength = 10

var usernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,31}$`)

// ValidUsername: 2–32 lowercase letters, digits, dot, dash or underscore.
func ValidUsername(u string) bool { return usernameRe.MatchString(u) }

// CheckPassword returns a sentence describing what's wrong with a new password, or "".
func CheckPassword(pw, username string) string {
	switch {
	case len([]rune(pw)) < MinPasswordLength:
		return fmt.Sprintf("Use at least %d characters.", MinPasswordLength)
	case strings.EqualFold(pw, username):
		return "The password can't be the username."
	case len([]rune(pw)) > 200:
		return "That password is too long."
	}
	return ""
}

// Hash returns "pbkdf2-sha256$<iterations>$<salt>$<key>".
func Hash(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, Iterations, 32)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("%s%d$%s$%s", hashPrefix, Iterations, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// IsHash reports whether s is a hash produced by Hash.
func IsHash(s string) bool { return strings.HasPrefix(s, hashPrefix) }

// Verify checks a password against a hash. A stored value that isn't a
// hash is compared as plain text (passwords typed into the config file).
func Verify(password, stored string) bool {
	if !IsHash(stored) {
		return stored != "" && subtle.ConstantTimeCompare([]byte(password), []byte(stored)) == 1
	}
	parts := strings.Split(strings.TrimPrefix(stored, hashPrefix), "$")
	if len(parts) != 3 {
		return false
	}
	iter, err := strconv.Atoi(parts[0])
	if err != nil || iter < 1 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[1])
	want, err2 := enc.DecodeString(parts[2])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// Session is the content of a signed session cookie.
type Session struct {
	Username string
	Epoch    int
	Expires  time.Time
}

var ErrBadSession = errors.New("invalid or expired session")

// Sign encodes a session as "<payload>.<hmac>".
func Sign(secret []byte, s Session) string {
	nonce := make([]byte, 8)
	rand.Read(nonce)
	payload := fmt.Sprintf("%s|%d|%d|%s", s.Username, s.Epoch, s.Expires.Unix(), base64.RawURLEncoding.EncodeToString(nonce))
	p := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return p + "." + base64.RawURLEncoding.EncodeToString(mac(secret, p))
}

// Parse verifies a signed session and checks its expiry.
func Parse(secret []byte, value string, now time.Time) (Session, error) {
	p, sig, ok := strings.Cut(value, ".")
	if !ok {
		return Session{}, ErrBadSession
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, mac(secret, p)) {
		return Session{}, ErrBadSession
	}
	raw, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return Session{}, ErrBadSession
	}
	f := strings.Split(string(raw), "|")
	if len(f) != 4 {
		return Session{}, ErrBadSession
	}
	epoch, err1 := strconv.Atoi(f[1])
	exp, err2 := strconv.ParseInt(f[2], 10, 64)
	if err1 != nil || err2 != nil {
		return Session{}, ErrBadSession
	}
	s := Session{Username: f[0], Epoch: epoch, Expires: time.Unix(exp, 0)}
	if !now.Before(s.Expires) {
		return Session{}, ErrBadSession
	}
	return s, nil
}

func mac(secret []byte, msg string) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(msg))
	return h.Sum(nil)
}

// NewSecret returns 32 random bytes for signing sessions.
func NewSecret() []byte {
	b := make([]byte, 32)
	rand.Read(b)
	return b
}

// RandomToken returns a URL-safe random string (setup links).
func RandomToken() string {
	b := make([]byte, 18)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Limiter locks out a username (per client address) after repeated failed
// sign-ins, and a client address after many failures across usernames.
type Limiter struct {
	MaxPerUser int           // failures per username+address before lockout
	MaxPerAddr int           // failures per address before lockout
	Window     time.Duration // failures older than this are forgotten
	Lockout    time.Duration

	mu       sync.Mutex
	failures map[string][]time.Time
	locked   map[string]time.Time
}

func NewLimiter() *Limiter {
	return &Limiter{MaxPerUser: 5, MaxPerAddr: 20, Window: 15 * time.Minute, Lockout: 15 * time.Minute,
		failures: map[string][]time.Time{}, locked: map[string]time.Time{}}
}

func userKey(username, addr string) string { return "u|" + strings.ToLower(username) + "|" + addr }
func addrKey(addr string) string           { return "a|" + addr }

// Blocked reports how long until this username+address may try again (0 = allowed).
func (l *Limiter) Blocked(username, addr string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	var wait time.Duration
	for _, k := range []string{userKey(username, addr), addrKey(addr)} {
		if until, ok := l.locked[k]; ok {
			if now.Before(until) {
				if d := until.Sub(now); d > wait {
					wait = d
				}
			} else {
				delete(l.locked, k)
			}
		}
	}
	return wait
}

// maxEntries bounds the limiter's memory; older entries are pruned first.
const maxEntries = 10000

// prune drops failures outside the window and expired lockouts.
func (l *Limiter) prune(now time.Time) {
	for k, ts := range l.failures {
		if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= l.Window {
			delete(l.failures, k)
		}
	}
	for k, until := range l.locked {
		if !now.Before(until) {
			delete(l.locked, k)
		}
	}
	// Still too many (a flood from many addresses): forget failures down to
	// half the cap, so pruning runs rarely. Lockouts are kept.
	for k := range l.failures {
		if len(l.failures)+len(l.locked) <= maxEntries/2 {
			break
		}
		delete(l.failures, k)
	}
}

// Fail records a failed sign-in and reports whether it caused a lockout.
func (l *Limiter) Fail(username, addr string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.failures)+len(l.locked) > maxEntries {
		l.prune(now)
	}
	locked := false
	for _, kv := range []struct {
		key string
		max int
	}{{userKey(username, addr), l.MaxPerUser}, {addrKey(addr), l.MaxPerAddr}} {
		var recent []time.Time
		for _, t := range l.failures[kv.key] {
			if now.Sub(t) < l.Window {
				recent = append(recent, t)
			}
		}
		recent = append(recent, now)
		if len(recent) >= kv.max {
			l.locked[kv.key] = now.Add(l.Lockout)
			delete(l.failures, kv.key)
			locked = true
			continue
		}
		l.failures[kv.key] = recent
	}
	return locked
}

// Succeed clears the failures for a username+address.
func (l *Limiter) Succeed(username, addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, userKey(username, addr))
}
