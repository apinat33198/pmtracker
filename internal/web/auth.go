package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/apinat33198/pmtracker/internal/auth"
	"github.com/apinat33198/pmtracker/internal/config"
	"github.com/apinat33198/pmtracker/internal/core"
)

const sessionCookie = "pmt_session"

// keepSignedIn is how long "Keep me signed in" lasts.
const keepSignedIn = 30 * 24 * time.Hour

type ctxKey struct{}

// user is whoever made the request. Anonymous visitors have no name and
// either no role or viewer, depending on auth.anonymous_access.
type user struct {
	Name     string
	Role     string
	SignedIn bool
	Config   bool // the account is defined in the config file
}

func (u user) can(role string) bool {
	return u.Role != "" && config.RoleRank(u.Role) >= config.RoleRank(role)
}

func userOf(r *http.Request) user {
	if u, ok := r.Context().Value(ctxKey{}).(user); ok {
		return u
	}
	return user{}
}

// account is a user from the data file or the config file.
type account struct {
	Username  string
	Role      string
	Hash      string
	Epoch     int
	Config    bool
	Created   time.Time
	LastLogin time.Time
}

// accounts lists config-file accounts first, then data-file ones. A
// config account wins over a data-file account with the same name.
func (s *Server) accounts() []account {
	var out []account
	seen := map[string]bool{}
	for _, u := range s.Cfg.Auth.Users {
		out = append(out, account{Username: u.Username, Role: u.Role, Hash: u.Password, Config: true, Epoch: fingerprint(u.Password)})
		seen[u.Username] = true
	}
	for _, u := range s.Store.Users() {
		if !seen[u.Username] {
			out = append(out, account{Username: u.Username, Role: u.Role, Hash: u.PasswordHash, Epoch: u.Epoch,
				Created: u.Created, LastLogin: u.LastLogin})
		}
	}
	return out
}

// fingerprint ties sessions of config-file accounts to their password:
// changing the password in the config ends the old sessions.
func fingerprint(secret string) int {
	sum := sha256.Sum256([]byte("pmtracker-config-account|" + secret))
	return int(binary.BigEndian.Uint32(sum[:4]) & 0x7fffffff)
}

func (s *Server) account(name string) (account, bool) {
	for _, a := range s.accounts() {
		if a.Username == name {
			return a, true
		}
	}
	return account{}, false
}

func (s *Server) adminCount() int {
	n := 0
	for _, a := range s.accounts() {
		if a.Role == config.RoleAdmin {
			n++
		}
	}
	return n
}

// needsSetup is true until the first account exists.
func (s *Server) needsSetup() bool {
	return !s.Demo && len(s.Cfg.Auth.Users) == 0 && len(s.Store.Users()) == 0
}

func (s *Server) initAuth() error {
	secret, err := s.Store.SessionSecret()
	if err != nil {
		return err
	}
	s.secret = secret
	s.limiter = auth.NewLimiter()
	for _, u := range s.Cfg.Auth.Users {
		if !auth.IsHash(u.Password) {
			s.Log.Printf("auth: %s has a plain-text password in the config file; replace it with the output of \"pmtracker hash-password\"", u.Username)
		}
	}
	s.setupLink()
	return nil
}

// setupLink creates the one-time setup token when there are no accounts
// and prints the link to the server log.
func (s *Server) setupLink() {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if !s.needsSetup() || s.setupToken != "" {
		return
	}
	s.setupToken = auth.RandomToken()
	s.Log.Printf("No accounts yet. Create the first admin at %s/setup?token=%s", s.publicBase(), s.setupToken)
}

func (s *Server) publicBase() string {
	if s.Cfg.PublicURL != "" {
		return strings.TrimRight(s.Cfg.PublicURL, "/")
	}
	if strings.HasPrefix(s.Cfg.Listen, ":") {
		return "http://localhost" + s.Cfg.Listen
	}
	return "http://" + s.Cfg.Listen
}

// authenticate identifies the user from a session cookie or a basic-auth
// header (for scripts), sends everyone to /setup until an account exists,
// rejects cross-site form posts, and sets protective headers.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'")
		h.Set("Referrer-Policy", "same-origin")
		if s.Cfg.HTTPS() {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		path := r.URL.Path
		// Nothing here takes large uploads except the CSV import.
		limit := int64(1 << 20)
		switch path {
		case "/admin/import":
			limit = 6 << 20
		case "/admin/backups/upload":
			limit = 32 << 20
		case "/login", "/setup":
			limit = 64 << 10
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		if path == "/healthz" || path == "/forecast.ics" || strings.HasPrefix(path, "/static/") {
			next.ServeHTTP(w, r) // the calendar feed checks its own token
			return
		}
		h.Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.sameOrigin(r) {
			http.Error(w, "Cross-site form posts are not allowed.", http.StatusForbidden)
			return
		}
		if s.needsSetup() && path != "/setup" {
			s.setupLink()
			if r.Method == http.MethodGet {
				http.Redirect(w, r, "/setup", http.StatusSeeOther)
				return
			}
			http.Error(w, "Create the first admin account first: see /setup.", http.StatusServiceUnavailable)
			return
		}
		u, msg := s.identify(r)
		if msg != "" {
			h.Set("WWW-Authenticate", `Basic realm="PM Tracker", charset="UTF-8"`)
			http.Error(w, msg, http.StatusUnauthorized)
			return
		}
		// Read forms up front so an oversized one is refused rather than
		// handled as empty. Posts from visitors who haven't signed in are only
		// read on the sign-in and setup pages.
		if r.Method == http.MethodPost && (u.SignedIn || path == "/login" || path == "/setup") &&
			strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			if err := r.ParseForm(); err != nil {
				code := http.StatusBadRequest
				var tooBig *http.MaxBytesError
				if errors.As(err, &tooBig) {
					code = http.StatusRequestEntityTooLarge
				}
				http.Error(w, "The form could not be read: "+err.Error(), code)
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	})
}

// sameOrigin rejects form posts sent from other sites (CSRF). Browsers
// send Sec-Fetch-Site and Origin with form posts; scripts send neither.
func (s *Server) sameOrigin(r *http.Request) bool {
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs == "cross-site" || sfs == "same-site" {
		return false
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	ou, err := url.Parse(o)
	if err != nil {
		return false
	}
	if ou.Host == r.Host {
		return true
	}
	pu, err := url.Parse(s.Cfg.PublicURL)
	return err == nil && s.Cfg.PublicURL != "" && ou.Scheme == pu.Scheme && ou.Host == pu.Host
}

// identify returns the user, or a message when basic-auth credentials are wrong.
func (s *Server) identify(r *http.Request) (user, string) {
	if name, pass, ok := r.BasicAuth(); ok {
		if s.plainHTTP(r) {
			return user{}, "Passwords aren't accepted over an unencrypted connection. Use " + s.publicBase() + "."
		}
		addr := s.clientAddr(r)
		if wait := s.limiter.Blocked(name, addr, s.Now()); wait > 0 {
			return user{}, "Too many failed sign-ins. Try again in " + minutes(wait) + "."
		}
		a, ok := s.checkPassword(name, pass)
		if !ok {
			if _, known := s.account(name); known {
				s.Store.LogFrom(name, addr, "Sign-in failed (script access): wrong password")
			}
			s.failed(name, addr)
			return user{}, "Wrong username or password."
		}
		s.limiter.Succeed(name, addr)
		return user{Name: a.Username, Role: a.Role, SignedIn: true, Config: a.Config}, ""
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		if sess, err := auth.Parse(s.secret, c.Value, s.Now()); err == nil {
			if a, ok := s.account(sess.Username); ok && a.Epoch == sess.Epoch {
				return user{Name: a.Username, Role: a.Role, SignedIn: true, Config: a.Config}, ""
			}
		}
	}
	if s.Cfg.Auth.AnonymousAccess == config.AnonymousViewer {
		return user{Role: config.RoleViewer}, ""
	}
	return user{}, ""
}

// need lets the request through when the user has at least the role.
// Visitors who haven't signed in are sent to the sign-in page.
func (s *Server) need(role string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := userOf(r)
		if u.can(role) {
			h(w, r)
			return
		}
		if !u.SignedIn {
			s.toLogin(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		s.renderCode(w, r, http.StatusForbidden, "denied", "", "Not allowed", s.snapshot(), map[string]string{
			"Need": role, "Role": u.Role, "Name": u.Name})
	})
}

// toLogin sends a visitor who hasn't signed in to the sign-in page. For a
// form post it returns them to the page they posted from; the body of a
// post from someone not signed in is never read.
func (s *Server) toLogin(w http.ResponseWriter, r *http.Request) {
	next := r.URL.RequestURI()
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		next = "/"
		if ref, err := url.Parse(r.Referer()); err == nil && ref.Host == r.Host {
			next = ref.RequestURI()
		}
	}
	http.Redirect(w, r, "/login?next="+url.QueryEscape(safeNext(next)), http.StatusSeeOther)
}

// localPath returns target when it's a path on this site. Anything that
// could lead a browser elsewhere is refused: other schemes and hosts,
// "//host", backslashes, and control characters (browsers drop tabs and
// newlines, so "/\t/evil.example" would become "//evil.example").
func localPath(target string) (string, bool) {
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.ContainsRune(target, '\\') {
		return "", false
	}
	for _, c := range target {
		if c < 0x20 || c == 0x7f {
			return "", false
		}
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" || u.User != nil {
		return "", false
	}
	return target, true
}

// safeNext is where to go after signing in.
func safeNext(next string) string {
	p, ok := localPath(next)
	if !ok || p == "/login" || strings.HasPrefix(p, "/login?") || strings.HasPrefix(p, "/setup") || strings.HasPrefix(p, "/logout") {
		return "/"
	}
	return p
}

// clientAddr is the caller's address for rate limiting. Requests from a
// trusted proxy are attributed to the first untrusted address in
// X-Forwarded-For, read from the right. IPv6 clients are grouped by /64,
// since one host usually controls a whole /64.
func (s *Server) clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	addr = addr.Unmap()
	if s.Cfg.TrustedProxy(addr) {
		var hops []string
		for _, v := range r.Header.Values("X-Forwarded-For") {
			hops = append(hops, strings.Split(v, ",")...)
		}
		for i := len(hops) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
			if err != nil {
				break
			}
			addr = a.Unmap()
			if !s.Cfg.TrustedProxy(addr) {
				break
			}
		}
	}
	if addr.Is6() {
		if p, err := addr.Prefix(64); err == nil {
			return p.String()
		}
	}
	return addr.String()
}

// plainHTTP reports a request that came straight from another machine over
// unencrypted HTTP while the tracker is published over HTTPS, such as
// someone opening http://<LAN address>:8080. Passwords typed there would
// cross the network readable, so sign-in is refused and pointed to the
// HTTPS address. Requests from this machine or from a trusted proxy, and
// requests a proxy marks as HTTPS, are fine.
func (s *Server) plainHTTP(r *http.Request) bool {
	if !s.Cfg.HTTPS() || r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	a = a.Unmap()
	return !a.IsLoopback() && !s.Cfg.TrustedProxy(a)
}

func (s *Server) isHTTPS(r *http.Request) bool {
	return r.TLS != nil || s.Cfg.HTTPS() || r.Header.Get("X-Forwarded-Proto") == "https"
}

func minutes(d time.Duration) string {
	m := int(d.Round(time.Minute) / time.Minute)
	if m < 1 {
		m = 1
	}
	return fmt.Sprintf("%d %s", m, plural(m, "minute", "minutes"))
}

// checkPassword verifies a username and password. Unknown usernames take
// as long as known ones.
func (s *Server) checkPassword(name, pass string) (account, bool) {
	a, ok := s.account(name)
	if !ok || pass == "" {
		s.dummyOnce.Do(func() { s.dummyHash, _ = auth.Hash(auth.RandomToken()) })
		auth.Verify(pass, s.dummyHash)
		return account{}, false
	}
	return a, auth.Verify(pass, a.Hash)
}

// failed records a failed sign-in and reports whether it locked the account.
func (s *Server) failed(name, addr string) bool {
	if !s.limiter.Fail(name, addr, s.Now()) {
		return false
	}
	_, known := s.account(name)
	if r := []rune(name); len(r) > 40 {
		name = string(r[:40]) + "…"
	}
	msg := fmt.Sprintf("Blocked sign-ins as %q for %s after repeated wrong passwords", name, minutes(s.limiter.Lockout))
	s.Log.Printf("auth: %s (from %s)", msg, addr)
	if known { // guesses at made-up usernames go to the server log only, so they can't flood Activity
		s.Store.LogFrom("security", addr, msg)
	}
	return true
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, a account, expires time.Time, persistent bool) {
	c := &http.Cookie{
		Name:     sessionCookie,
		Value:    auth.Sign(s.secret, auth.Session{Username: a.Username, Epoch: a.Epoch, Expires: expires}),
		Path:     "/",
		HttpOnly: true,
		Secure:   s.isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	}
	if persistent {
		c.MaxAge = int(expires.Sub(s.Now()).Seconds())
	}
	http.SetCookie(w, c)
}

func (s *Server) signIn(w http.ResponseWriter, r *http.Request, a account, keep bool) {
	d := time.Duration(s.Cfg.Auth.SessionHours) * time.Hour
	if keep {
		d = keepSignedIn
	}
	s.startSession(w, r, a, s.Now().Add(d), keep)
}

// reissue replaces the current session cookie after the account's epoch
// changed, keeping its expiry.
func (s *Server) reissue(w http.ResponseWriter, r *http.Request, name string) {
	a, ok := s.account(name)
	if !ok {
		return
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return // basic auth
	}
	sess, err := auth.Parse(s.secret, c.Value, s.Now())
	if err != nil {
		return
	}
	persistent := sess.Expires.Sub(s.Now()) > time.Duration(s.Cfg.Auth.SessionHours)*time.Hour
	s.startSession(w, r, a, sess.Expires, persistent)
}

func (s *Server) clearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: s.isHTTPS(r), SameSite: http.SameSiteLaxMode})
}

// --- sign in, sign out, first-run setup ---

type loginData struct {
	Next, Username, Error string
	Keep, Demo            bool
	SecureURL             string // set when this page was opened over plain HTTP
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	if userOf(r).SignedIn {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	d := loginData{Next: next, Demo: s.Demo}
	if s.plainHTTP(r) {
		d.SecureURL = s.publicBase() + "/login?next=" + url.QueryEscape(next)
	}
	s.renderCode(w, r, http.StatusOK, "login", "", "Sign in", core.Snapshot{}, d)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.FormValue("username")))
	d := loginData{Next: safeNext(r.FormValue("next")), Username: name, Keep: r.FormValue("keep") != "", Demo: s.Demo}
	if s.plainHTTP(r) {
		d.SecureURL = s.publicBase() + "/login?next=" + url.QueryEscape(d.Next)
		s.renderCode(w, r, http.StatusForbidden, "login", "", "Sign in", core.Snapshot{}, d)
		return
	}
	addr := s.clientAddr(r)
	if wait := s.limiter.Blocked(name, addr, s.Now()); wait > 0 {
		d.Error = "Too many failed sign-ins. Try again in " + minutes(wait) + "."
		s.renderCode(w, r, http.StatusTooManyRequests, "login", "", "Sign in", core.Snapshot{}, d)
		return
	}
	a, ok := s.checkPassword(name, r.FormValue("password"))
	if !ok {
		if _, known := s.account(name); known {
			s.Store.LogFrom(name, addr, "Sign-in failed: wrong password")
		}
		d.Error = "That username and password don't match an account."
		if s.failed(name, addr) {
			d.Error = "Too many failed sign-ins. Try again in " + minutes(s.limiter.Lockout) + "."
		}
		s.renderCode(w, r, http.StatusUnauthorized, "login", "", "Sign in", core.Snapshot{}, d)
		return
	}
	s.limiter.Succeed(name, addr)
	if !a.Config {
		s.Store.RecordLogin(a.Username, s.Now())
	}
	action := "Signed in"
	if d.Keep {
		action = "Signed in (kept for 30 days)"
	}
	s.Store.LogFrom(a.Username, addr, action)
	s.signIn(w, r, a, d.Keep)
	http.Redirect(w, r, d.Next, http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if u := userOf(r); u.SignedIn {
		s.Store.LogFrom(u.Name, s.clientAddr(r), "Signed out")
	}
	s.clearSession(w, r)
	s.redirect(w, r, "/login", "You're signed out.", "")
}

type setupData struct {
	Token, Username, Error string
	Valid                  bool
	SecureURL              string
}

func (s *Server) validSetupToken(tok string) bool {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	return s.setupToken != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.setupToken)) == 1
}

func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	if !s.needsSetup() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.setupLink()
	tok := r.URL.Query().Get("token")
	code := http.StatusOK
	if !s.validSetupToken(tok) {
		code = http.StatusForbidden
	}
	d := setupData{Token: tok, Valid: code == http.StatusOK}
	if s.plainHTTP(r) {
		d.SecureURL = s.publicBase() + "/setup?token=" + url.QueryEscape(tok)
	}
	s.renderCode(w, r, code, "setup", "", "Create the first admin", core.Snapshot{}, d)
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if !s.needsSetup() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	tok := r.FormValue("token")
	d := setupData{Token: tok, Valid: s.setupToken != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.setupToken)) == 1}
	if s.plainHTTP(r) {
		d.SecureURL = s.publicBase() + "/setup?token=" + url.QueryEscape(tok)
		s.renderCode(w, r, http.StatusForbidden, "setup", "", "Create the first admin", core.Snapshot{}, d)
		return
	}
	if !d.Valid {
		s.renderCode(w, r, http.StatusForbidden, "setup", "", "Create the first admin", core.Snapshot{}, d)
		return
	}
	d.Username = strings.ToLower(strings.TrimSpace(r.FormValue("username")))
	if d.Error = checkNewAccount(d.Username, r.FormValue("password"), r.FormValue("confirm")); d.Error != "" {
		s.renderCode(w, r, http.StatusUnprocessableEntity, "setup", "", "Create the first admin", core.Snapshot{}, d)
		return
	}
	hash, err := auth.Hash(r.FormValue("password"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	u := auth.User{Username: d.Username, Role: config.RoleAdmin, PasswordHash: hash, Created: s.Now(), LastLogin: s.Now()}
	if err := s.Store.SaveUser(u, d.Username, "Created the first admin account, "+d.Username); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.setupToken = ""
	s.Log.Printf("Setup finished: %s is the first admin.", d.Username)
	s.signIn(w, r, account{Username: u.Username, Role: u.Role}, false)
	s.redirect(w, r, "/admin/calendars", "Welcome. Next, add the Google calendars to read, then add your team under Admin, Users.", "")
}

// checkNewAccount validates a username and a new password typed twice.
func checkNewAccount(name, pw, confirm string) string {
	if !auth.ValidUsername(name) {
		return "Usernames are 2 to 32 lowercase letters, digits, dots, dashes or underscores."
	}
	if msg := auth.CheckPassword(pw, name); msg != "" {
		return msg
	}
	if pw != confirm {
		return "The two passwords don't match."
	}
	return ""
}

// --- your account ---

func (s *Server) accountPage(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	if !u.SignedIn {
		s.toLogin(w, r)
		return
	}
	a, _ := s.account(u.Name)
	s.render(w, r, "account", "account", "Your account", s.snapshot(), a)
}

func (s *Server) changeOwnPassword(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	if !u.SignedIn {
		s.toLogin(w, r)
		return
	}
	if u.Config {
		s.redirect(w, r, "/account", "This account is defined in the config file. Change its password there.", "error")
		return
	}
	addr := s.clientAddr(r)
	if wait := s.limiter.Blocked(u.Name, addr, s.Now()); wait > 0 {
		s.redirect(w, r, "/account", "Too many wrong passwords. Try again in "+minutes(wait)+".", "error")
		return
	}
	if _, ok := s.checkPassword(u.Name, r.FormValue("current")); !ok {
		s.failed(u.Name, addr)
		s.redirect(w, r, "/account", "Your current password isn't right.", "error")
		return
	}
	pw := r.FormValue("password")
	if msg := auth.CheckPassword(pw, u.Name); msg != "" {
		s.redirect(w, r, "/account", msg, "error")
		return
	}
	if pw != r.FormValue("confirm") {
		s.redirect(w, r, "/account", "The two new passwords don't match.", "error")
		return
	}
	if err := s.setPassword(u.Name, pw, u.Name, "Changed their own password"); err != nil {
		s.redirect(w, r, "/account", err.Error(), "error")
		return
	}
	s.reissue(w, r, u.Name)
	s.redirect(w, r, "/account", "Password changed. Other devices signed in to your account have been signed out.", "")
}

func (s *Server) signOutOthers(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	if !u.SignedIn {
		s.toLogin(w, r)
		return
	}
	su, ok := s.Store.User(u.Name)
	if u.Config || !ok {
		s.redirect(w, r, "/account", "This account is defined in the config file, so its sessions can't be ended from here.", "error")
		return
	}
	su.Epoch++
	if err := s.Store.SaveUser(su, u.Name, "Signed out of their other devices"); err != nil {
		s.redirect(w, r, "/account", err.Error(), "error")
		return
	}
	s.reissue(w, r, u.Name)
	s.redirect(w, r, "/account", "Signed out everywhere else. You're still signed in here.", "")
}

// setPassword stores a new hash and ends the account's other sessions.
func (s *Server) setPassword(name, pw, by, action string) error {
	su, ok := s.Store.User(name)
	if !ok {
		return fmt.Errorf("there's no account called %s", name)
	}
	hash, err := auth.Hash(pw)
	if err != nil {
		return err
	}
	su.PasswordHash = hash
	su.Epoch++
	return s.Store.SaveUser(su, by, action)
}

// --- Admin, Users ---

type usersData struct {
	Accounts []account
	Me       string
	Admins   int
	Plain    []string // config accounts with plain-text passwords
	Roles    []string
	Anon     string
}

func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request) {
	d := usersData{Accounts: s.accounts(), Me: userOf(r).Name, Admins: s.adminCount(),
		Roles: []string{config.RoleViewer, config.RoleEditor, config.RoleAdmin}, Anon: s.Cfg.Auth.AnonymousAccess}
	for _, a := range d.Accounts {
		if a.Config && !auth.IsHash(a.Hash) {
			d.Plain = append(d.Plain, a.Username)
		}
	}
	s.render(w, r, "admin_users", "admin", "Users", s.snapshot(), d)
}

func (s *Server) adminAddUser(w http.ResponseWriter, r *http.Request) {
	by := userOf(r).Name
	name := strings.ToLower(strings.TrimSpace(r.FormValue("username")))
	role, pw := r.FormValue("role"), r.FormValue("password")
	if !auth.ValidUsername(name) {
		s.redirect(w, r, "/admin/users", "Usernames are 2 to 32 lowercase letters, digits, dots, dashes or underscores.", "error")
		return
	}
	if _, exists := s.account(name); exists {
		s.redirect(w, r, "/admin/users", "There's already an account called "+name+".", "error")
		return
	}
	if config.RoleRank(role) < 0 {
		s.redirect(w, r, "/admin/users", "Pick a role for the new account.", "error")
		return
	}
	if msg := auth.CheckPassword(pw, name); msg != "" {
		s.redirect(w, r, "/admin/users", msg, "error")
		return
	}
	hash, err := auth.Hash(pw)
	if err != nil {
		s.redirect(w, r, "/admin/users", err.Error(), "error")
		return
	}
	u := auth.User{Username: name, Role: role, PasswordHash: hash, Created: s.Now()}
	if err := s.Store.SaveUser(u, by, fmt.Sprintf("Added the account %s (%s)", name, role)); err != nil {
		s.redirect(w, r, "/admin/users", err.Error(), "error")
		return
	}
	s.redirect(w, r, "/admin/users", fmt.Sprintf("Added %s as %s %s. Give them the password; they can change it under Your account.", name, article(role), role), "")
}

// editable returns the data-file account named in the path, or redirects with why it can't be changed.
func (s *Server) editable(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
	name := r.PathValue("name")
	a, ok := s.account(name)
	if !ok {
		s.redirect(w, r, "/admin/users", "That account no longer exists.", "error")
		return auth.User{}, false
	}
	if a.Config {
		s.redirect(w, r, "/admin/users", name+" is defined in the config file. Change it there.", "error")
		return auth.User{}, false
	}
	su, _ := s.Store.User(name)
	return su, true
}

func (s *Server) adminSetRole(w http.ResponseWriter, r *http.Request) {
	me := userOf(r).Name
	su, ok := s.editable(w, r)
	if !ok {
		return
	}
	role := r.FormValue("role")
	if config.RoleRank(role) < 0 {
		s.redirect(w, r, "/admin/users", "Pick viewer, editor or admin.", "error")
		return
	}
	if role == su.Role {
		s.redirect(w, r, "/admin/users", su.Username+" is already "+article(role)+" "+role+".", "")
		return
	}
	if su.Role == config.RoleAdmin && s.adminCount() <= 1 {
		s.redirect(w, r, "/admin/users", su.Username+" is the only admin. Make someone else an admin first.", "error")
		return
	}
	old := su.Role
	su.Role = role
	if err := s.Store.SaveUser(su, me, fmt.Sprintf("Changed %s's role from %s to %s", su.Username, old, role)); err != nil {
		s.redirect(w, r, "/admin/users", err.Error(), "error")
		return
	}
	if su.Username == me && role != config.RoleAdmin {
		s.redirect(w, r, "/", "You're now "+article(role)+" "+role+", so the admin pages are closed to you.", "")
		return
	}
	s.redirect(w, r, "/admin/users", fmt.Sprintf("%s is now %s %s.", su.Username, article(role), role), "")
}

func (s *Server) adminResetPassword(w http.ResponseWriter, r *http.Request) {
	me := userOf(r).Name
	su, ok := s.editable(w, r)
	if !ok {
		return
	}
	pw := r.FormValue("password")
	if msg := auth.CheckPassword(pw, su.Username); msg != "" {
		s.redirect(w, r, "/admin/users", msg, "error")
		return
	}
	if err := s.setPassword(su.Username, pw, me, "Set a new password for "+su.Username); err != nil {
		s.redirect(w, r, "/admin/users", err.Error(), "error")
		return
	}
	if su.Username == me {
		s.reissue(w, r, me)
	}
	s.redirect(w, r, "/admin/users", "Set a new password for "+su.Username+" and signed them out everywhere.", "")
}

func (s *Server) adminDeleteUser(w http.ResponseWriter, r *http.Request) {
	me := userOf(r).Name
	su, ok := s.editable(w, r)
	if !ok {
		return
	}
	if su.Username == me {
		s.redirect(w, r, "/admin/users", "You can't delete your own account. Ask another admin.", "error")
		return
	}
	if su.Role == config.RoleAdmin && s.adminCount() <= 1 {
		s.redirect(w, r, "/admin/users", su.Username+" is the only admin. Make someone else an admin first.", "error")
		return
	}
	if err := s.Store.DeleteUser(su.Username, me); err != nil {
		s.redirect(w, r, "/admin/users", err.Error(), "error")
		return
	}
	s.redirect(w, r, "/admin/users", "Deleted "+su.Username+". They've been signed out.", "")
}

func article(role string) string {
	if role != "" && strings.ContainsAny(role[:1], "aeiou") {
		return "an"
	}
	return "a"
}
