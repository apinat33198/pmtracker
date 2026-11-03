package web

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/apinat33198/pmtracker/internal/auth"
	"github.com/apinat33198/pmtracker/internal/config"
)

// browser is a client with a cookie jar that doesn't follow redirects.
type browser struct {
	t    *testing.T
	base string
	c    *http.Client
}

func newBrowser(t *testing.T, e env) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, base: e.srv.URL, c: &http.Client{Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// do returns the status, body and the Location header.
func (b *browser) do(method, path string, form url.Values, header ...string) (int, string, string) {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r, _ := http.NewRequest(method, b.base+path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	resp, err := b.c.Do(r)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out), resp.Header.Get("Location")
}

func (b *browser) login(name, pw string) (int, string) {
	b.t.Helper()
	code, body, loc := b.do("POST", "/login", url.Values{"username": {name}, "password": {pw}, "next": {"/admin/sites"}})
	if code == http.StatusSeeOther {
		return code, loc
	}
	return code, body
}

func (b *browser) session() *http.Cookie {
	u, _ := url.Parse(b.base)
	for _, c := range b.c.Jar.Cookies(u) {
		if c.Name == sessionCookie {
			return c
		}
	}
	return nil
}

func addUser(t *testing.T, e env, name, role, pw string) {
	t.Helper()
	h, _ := auth.Hash(pw)
	if err := e.store.SaveUser(auth.User{Username: name, Role: role, PasswordHash: h}, "test", ""); err != nil {
		t.Fatal(err)
	}
}

func TestSignInFlow(t *testing.T) {
	e := demoServer(t, nil)
	addUser(t, e, "somchai", config.RoleAdmin, "correct horse battery")
	addUser(t, e, "nok", config.RoleViewer, "viewer password 1")
	b := newBrowser(t, e)

	code, _, loc := b.do("GET", "/admin/sites?region=East", nil)
	if code != http.StatusSeeOther || loc != "/login?next=%2Fadmin%2Fsites%3Fregion%3DEast" {
		t.Fatalf("anonymous admin page = %d %q", code, loc)
	}
	if code, body, _ := b.do("GET", "/login?next=/admin/sites", nil); code != 200 || !strings.Contains(body, `name="password"`) || strings.Contains(body, "Review inbox") {
		t.Errorf("sign-in page = %d, should be a bare form", code)
	}
	if code, body := b.login("somchai", "wrong password!"); code != http.StatusUnauthorized || !strings.Contains(body, "don&#39;t match") {
		t.Errorf("wrong password = %d", code)
	}
	if code, body := b.login("ghost", "whatever123"); code != http.StatusUnauthorized || !strings.Contains(body, "don&#39;t match") {
		t.Errorf("unknown user = %d", code)
	}
	if code, loc := b.login("Somchai ", "correct horse battery"); code != http.StatusSeeOther || loc != "/admin/sites" {
		t.Fatalf("sign in = %d %q", code, loc)
	}
	c := b.session()
	if c == nil {
		t.Fatal("no session cookie")
	}
	if code, body, _ := b.do("GET", "/admin/sites", nil); code != 200 || !strings.Contains(body, `href="/account"`) || !strings.Contains(body, "Sign out") {
		t.Errorf("signed-in admin page = %d", code)
	}
	if u, _ := e.store.User("somchai"); u.LastLogin.IsZero() {
		t.Error("last sign-in should be recorded")
	}

	// A tampered cookie is ignored.
	evil := newBrowser(t, e)
	u, _ := url.Parse(e.srv.URL)
	evil.c.Jar.SetCookies(u, []*http.Cookie{{Name: sessionCookie, Value: strings.Replace(c.Value, c.Value[:6], "AAAAAA", 1)}})
	if code, _, _ := evil.do("GET", "/admin/sites", nil); code != http.StatusSeeOther {
		t.Errorf("tampered cookie = %d, want a redirect to sign in", code)
	}

	// Cross-site posts are refused even with a valid session.
	form := url.Values{"event": {"facilities@demo|abc-survey"}}
	if code, _, _ := b.do("POST", "/inbox/ignore", form, "Origin", "https://evil.example"); code != http.StatusForbidden {
		t.Errorf("cross-origin post = %d", code)
	}
	if code, _, _ := b.do("POST", "/inbox/ignore", form, "Sec-Fetch-Site", "cross-site"); code != http.StatusForbidden {
		t.Errorf("cross-site post = %d", code)
	}
	if code, _, _ := b.do("POST", "/inbox/ignore", form, "Origin", e.srv.URL, "Sec-Fetch-Site", "same-origin"); code != http.StatusSeeOther {
		t.Errorf("same-origin post = %d", code)
	}

	// Sign out ends the session.
	if code, _, loc := b.do("POST", "/logout", url.Values{}); code != http.StatusSeeOther || !strings.HasPrefix(loc, "/login") {
		t.Errorf("sign out = %d %q", code, loc)
	}
	if code, _, _ := b.do("GET", "/admin/sites", nil); code != http.StatusSeeOther {
		t.Errorf("after sign out = %d", code)
	}

	// Viewers get a clear 403 on admin pages.
	v := newBrowser(t, e)
	v.login("nok", "viewer password 1")
	if code, body, _ := v.do("GET", "/admin/sites", nil); code != http.StatusForbidden || !strings.Contains(body, "needs admin access") {
		t.Errorf("viewer on admin = %d", code)
	}
	if code, _, _ := v.do("GET", "/", nil); code != 200 {
		t.Errorf("viewer on overview = %d", code)
	}

	// next can't send people to another site.
	for _, next := range []string{"//evil.example/x", "https://evil.example", `/\evil.example`, "/login"} {
		o := newBrowser(t, e)
		_, _, loc := o.do("POST", "/login", url.Values{"username": {"nok"}, "password": {"viewer password 1"}, "next": {next}})
		if loc != "/" {
			t.Errorf("next=%q redirected to %q", next, loc)
		}
	}
}

func TestLockout(t *testing.T) {
	e := demoServer(t, nil)
	addUser(t, e, "somchai", config.RoleAdmin, "correct horse battery")
	b := newBrowser(t, e)
	for i := 0; i < 5; i++ {
		b.login("somchai", "guess"+string(rune('a'+i)))
	}
	if code, body := b.login("somchai", "correct horse battery"); code != http.StatusTooManyRequests || !strings.Contains(body, "Try again in 15 minutes") {
		t.Errorf("locked account accepted the right password: %d", code)
	}
	if a := e.store.Audit(1); len(a) == 0 || !strings.Contains(a[0].Action, "Blocked sign-ins") {
		t.Errorf("lockout should be in the activity log: %+v", a)
	}
	// Scripts using basic auth are limited too.
	if code, _, _ := req(t, "GET", e.srv.URL+"/", "somchai", nil); code != http.StatusUnauthorized {
		t.Errorf("basic auth while locked = %d", code)
	}
	// Other accounts still work.
	if code, _, _ := req(t, "GET", e.srv.URL+"/", "admin", nil); code != 200 {
		t.Errorf("other account = %d", code)
	}
}

func TestFirstRunSetup(t *testing.T) {
	e := demoServerWith(t, []config.User{}, func(s *Server) { s.Demo = false })
	tok := e.server.setupToken
	if tok == "" {
		t.Fatal("no setup token")
	}
	b := newBrowser(t, e)
	for _, p := range []string{"/", "/admin/sites", "/login"} {
		if code, _, loc := b.do("GET", p, nil); code != http.StatusSeeOther || loc != "/setup" {
			t.Errorf("GET %s before setup = %d %q", p, code, loc)
		}
	}
	if code, body, _ := b.do("GET", "/setup", nil); code != http.StatusForbidden || !strings.Contains(body, "server's log") {
		t.Errorf("setup without token = %d", code)
	}
	if code, _, _ := b.do("POST", "/setup", url.Values{"token": {"nope"}, "username": {"x1"}, "password": {"0123456789"}, "confirm": {"0123456789"}}); code != http.StatusForbidden {
		t.Errorf("setup with a wrong token = %d", code)
	}
	if code, body, _ := b.do("POST", "/setup", url.Values{"token": {tok}, "username": {"somchai"}, "password": {"short"}, "confirm": {"short"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "at least 10") {
		t.Errorf("weak password = %d", code)
	}
	code, _, loc := b.do("POST", "/setup", url.Values{"token": {tok}, "username": {"somchai"}, "password": {"correct horse battery"}, "confirm": {"correct horse battery"}})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/admin/calendars") {
		t.Fatalf("setup = %d %q", code, loc)
	}
	if u, ok := e.store.User("somchai"); !ok || u.Role != config.RoleAdmin {
		t.Fatal("first account should be an admin")
	}
	if code, _, _ := b.do("GET", "/admin/users", nil); code != 200 {
		t.Errorf("signed in after setup = %d", code)
	}
	if code, _, loc := newBrowser(t, e).do("GET", "/setup?token="+tok, nil); code != http.StatusSeeOther || loc != "/login" {
		t.Errorf("setup after the first admin = %d %q", code, loc)
	}
}

func TestAnonymousViewers(t *testing.T) {
	e := demoServerWith(t, nil, func(s *Server) { s.Cfg.Auth.AnonymousAccess = config.AnonymousViewer })
	b := newBrowser(t, e)
	if code, body, _ := b.do("GET", "/", nil); code != 200 || !strings.Contains(body, `href="/login?next=%2f"`) || strings.Contains(body, "Sync now") {
		t.Errorf("anonymous overview = %d", code)
	}
	if code, _, _ := b.do("GET", "/admin/sites", nil); code != http.StatusSeeOther {
		t.Errorf("anonymous admin = %d", code)
	}
	if code, _, loc := b.do("POST", "/inbox/ignore", url.Values{"event": {"x"}, "back": {"/inbox"}}); code != http.StatusSeeOther || loc != "/login?next=%2Finbox" {
		t.Errorf("anonymous post = %d %q", code, loc)
	}
	if code, _, _ := b.do("GET", "/account", nil); code != http.StatusSeeOther {
		t.Errorf("anonymous account page = %d", code)
	}
}

func TestUserManagement(t *testing.T) {
	e := demoServer(t, []config.User{{Username: "breakglass", Password: "pw", Role: config.RoleAdmin}})
	addUser(t, e, "somchai", config.RoleAdmin, "correct horse battery")
	admin := newBrowser(t, e)
	admin.login("somchai", "correct horse battery")
	msg := func(loc string) string { u, _ := url.Parse(loc); return u.Query().Get("msg") }
	post := func(b *browser, path string, form url.Values) string {
		t.Helper()
		code, body, loc := b.do("POST", path, form)
		if code != http.StatusSeeOther {
			t.Fatalf("POST %s = %d: %.200s", path, code, body)
		}
		return msg(loc)
	}

	if m := post(admin, "/admin/users", url.Values{"username": {"nok s"}, "role": {"editor"}, "password": {"long enough pw"}}); !strings.Contains(m, "Usernames are") {
		t.Errorf("uppercase username: %q", m)
	}
	if m := post(admin, "/admin/users", url.Values{"username": {"nok.s"}, "role": {"editor"}, "password": {"short"}}); !strings.Contains(m, "at least 10") {
		t.Errorf("short password: %q", m)
	}
	if m := post(admin, "/admin/users", url.Values{"username": {"breakglass"}, "role": {"editor"}, "password": {"long enough pw"}}); !strings.Contains(m, "already an account") {
		t.Errorf("duplicate of a config user: %q", m)
	}
	if m := post(admin, "/admin/users", url.Values{"username": {"nok.s"}, "role": {"editor"}, "password": {"first password"}}); !strings.Contains(m, "Added nok.s as an editor") {
		t.Fatalf("add: %q", m)
	}
	nok := newBrowser(t, e)
	if code, _ := nok.login("nok.s", "first password"); code != http.StatusSeeOther {
		t.Fatal("new user can't sign in")
	}
	if code, _, _ := nok.do("GET", "/admin/users", nil); code != http.StatusForbidden {
		t.Errorf("editor on users page = %d", code)
	}

	// Role changes apply to the next request.
	post(admin, "/admin/users/nok.s/role", url.Values{"role": {"admin"}})
	if code, _, _ := nok.do("GET", "/admin/users", nil); code != 200 {
		t.Errorf("promoted user = %d", code)
	}
	post(admin, "/admin/users/nok.s/role", url.Values{"role": {"viewer"}})

	// Changing your own password needs the current one and ends other sessions.
	nok2 := newBrowser(t, e)
	nok2.login("nok.s", "first password")
	if m := post(nok, "/account/password", url.Values{"current": {"nope"}, "password": {"second password"}, "confirm": {"second password"}}); !strings.Contains(m, "isn't right") {
		t.Errorf("wrong current password: %q", m)
	}
	if m := post(nok, "/account/password", url.Values{"current": {"first password"}, "password": {"second password"}, "confirm": {"second password"}}); !strings.Contains(m, "Password changed") {
		t.Fatalf("change password: %q", m)
	}
	if code, _, _ := nok.do("GET", "/account", nil); code != 200 {
		t.Errorf("this device should stay signed in: %d", code)
	}
	if code, _, _ := nok2.do("GET", "/account", nil); code != http.StatusSeeOther {
		t.Errorf("other device should be signed out: %d", code)
	}
	if code, _ := newBrowser(t, e).login("nok.s", "second password"); code != http.StatusSeeOther {
		t.Error("new password should work")
	}

	// Sign out everywhere else.
	nok3 := newBrowser(t, e)
	nok3.login("nok.s", "second password")
	post(nok, "/account/signout-others", url.Values{})
	if code, _, _ := nok3.do("GET", "/", nil); code != http.StatusSeeOther {
		t.Errorf("signed out elsewhere: %d", code)
	}
	if code, _, _ := nok.do("GET", "/", nil); code != 200 {
		t.Errorf("still signed in here: %d", code)
	}

	// An admin resetting a password signs the user out.
	if m := post(admin, "/admin/users/nok.s/password", url.Values{"password": {"third password"}}); !strings.Contains(m, "signed them out") {
		t.Errorf("reset: %q", m)
	}
	if code, _, _ := nok.do("GET", "/", nil); code != http.StatusSeeOther {
		t.Errorf("after reset = %d", code)
	}

	// Config accounts are read-only here.
	if m := post(admin, "/admin/users/breakglass/role", url.Values{"role": {"viewer"}}); !strings.Contains(m, "config file") {
		t.Errorf("config user role: %q", m)
	}
	if m := post(admin, "/admin/users/somchai/delete", url.Values{}); !strings.Contains(m, "your own account") {
		t.Errorf("delete self: %q", m)
	}
	if m := post(admin, "/admin/users/nok.s/delete", url.Values{}); !strings.Contains(m, "Deleted nok.s") {
		t.Errorf("delete: %q", m)
	}
	if _, ok := e.store.User("nok.s"); ok {
		t.Error("nok.s should be gone")
	}
	body := func() string { _, b, _ := admin.do("GET", "/admin/users", nil); return b }()
	if !strings.Contains(body, "plain-text passwords for breakglass") || !strings.Contains(body, "Config file") {
		t.Error("users page should flag plain-text config passwords")
	}
	if a := e.store.Audit(20); !strings.Contains(a[0].Action, "Deleted the account nok.s") || a[0].By != "somchai" {
		t.Errorf("audit = %+v", a[0])
	}
}

func TestLastAdminGuard(t *testing.T) {
	e := demoServer(t, []config.User{})
	e.server.Demo = true
	addUser(t, e, "somchai", config.RoleAdmin, "correct horse battery")
	b := newBrowser(t, e)
	b.login("somchai", "correct horse battery")
	_, _, loc := b.do("POST", "/admin/users/somchai/role", url.Values{"role": {"editor"}})
	if u, _ := url.Parse(loc); !strings.Contains(u.Query().Get("msg"), "only admin") {
		t.Errorf("demoting the only admin: %q", loc)
	}
}

func TestKeepSignedIn(t *testing.T) {
	e := demoServer(t, nil)
	addUser(t, e, "somchai", config.RoleAdmin, "correct horse battery")
	r, _ := http.NewRequest("POST", e.srv.URL+"/login", strings.NewReader(url.Values{"username": {"somchai"}, "password": {"correct horse battery"}, "keep": {"on"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noFollow.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var c *http.Cookie
	for _, x := range resp.Cookies() {
		if x.Name == sessionCookie {
			c = x
		}
	}
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.MaxAge < int((29*24*time.Hour).Seconds()) {
		t.Errorf("persistent cookie = %+v", c)
	}
}
