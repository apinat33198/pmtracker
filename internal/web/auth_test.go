package web

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
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
	if code, _, loc := b.do("POST", "/inbox/ignore", url.Values{"event": {"x"}}, "Referer", e.srv.URL+"/inbox"); code != http.StatusSeeOther || loc != "/login?next=%2Finbox" {
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

func TestRedirectsStayOnSite(t *testing.T) {
	e := demoServer(t, nil)
	addUser(t, e, "nok", config.RoleEditor, "viewer password 1")
	bad := []string{"/\t/evil.example", "/\n/evil.example", "/\r\n/evil.example", "/\\evil.example", "/x\\..\\\\evil",
		"//evil.example", "https://evil.example", "javascript:alert(1)", "/\x7f", " /x", "/login", "/logout"}
	for _, next := range bad {
		b := newBrowser(t, e)
		_, _, loc := b.do("POST", "/login", url.Values{"username": {"nok"}, "password": {"viewer password 1"}, "next": {next}})
		if loc != "/" {
			t.Errorf("sign in with next=%q went to %q", next, loc)
		}
		if code, body, _ := b.do("GET", "/login?next="+url.QueryEscape(next), nil); code != http.StatusSeeOther && strings.Contains(body, "evil") {
			t.Errorf("sign-in form kept next=%q", next)
		}
		// Form "back" fields: no panic, no off-site redirect.
		code, _, loc := b.do("POST", "/visits/unmark", url.Values{"visit": {"x"}, "back": {next}})
		lu, err := url.Parse(loc)
		if code != http.StatusSeeOther || err != nil || lu.Host != "" || strings.HasPrefix(loc, "//") || strings.ContainsAny(loc, "\\\t\r\n") {
			t.Errorf("back=%q -> %d %q", next, code, loc)
		}
	}
	b := newBrowser(t, e)
	if _, _, loc := b.do("POST", "/login", url.Values{"username": {"nok"}, "password": {"viewer password 1"}, "next": {"/sites/bkk1?x=1"}}); loc != "/sites/bkk1?x=1" {
		t.Errorf("a normal next should be kept, got %q", loc)
	}
}

func TestBodyLimits(t *testing.T) {
	e := demoServer(t, nil)
	big := strings.Repeat("A", 2<<20)
	// Not signed in: the body of a post to a protected page is never read.
	r, _ := http.NewRequest("POST", e.srv.URL+"/admin/import", strings.NewReader("csv="+strings.Repeat("A", 20<<20)))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noFollow.Do(r)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Errorf("anonymous big post = %d", resp.StatusCode)
		}
	}
	if code, _, _ := newBrowser(t, e).do("POST", "/login", url.Values{"username": {"x"}, "password": {big}}); code != http.StatusRequestEntityTooLarge {
		t.Errorf("a 2 MB sign-in form should be refused, got %d", code)
	}
	// Signed in, ordinary forms are capped at 1 MB; the import takes up to 6 MB.
	if code, _, _ := req(t, "POST", e.srv.URL+"/admin/systems", "admin", url.Values{"name": {big}}); code != http.StatusRequestEntityTooLarge {
		t.Errorf("2 MB admin form = %d", code)
	}
	csv := "site_code,system,vendor\n" + strings.Repeat("ZZZ1,UPS,ABC Power\n", 150000)
	if code, body, _ := req(t, "POST", e.srv.URL+"/admin/import", "admin", url.Values{"csv": {csv}, "mode": {"check"}}); code != 200 || !strings.Contains(body, "Nothing was saved yet") {
		t.Errorf("a 3 MB import should be accepted: %d", code)
	}
}

func TestFlashMessagesAreSigned(t *testing.T) {
	e := demoServer(t, nil)
	if body := get(t, e, "/?msg=Your+account+is+locked.+Call+081-000-0000&kind=error"); strings.Contains(body, "081-000-0000") {
		t.Error("an unsigned message from the URL was shown")
	}
	code, _, _ := req(t, "POST", e.srv.URL+"/admin/systems", "admin", url.Values{"name": {"Battery"}})
	if code != http.StatusSeeOther {
		t.Fatal(code)
	}
	r, _ := http.NewRequest("POST", e.srv.URL+"/admin/systems", strings.NewReader("name=Battery+bank"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetBasicAuth("admin", "pw")
	resp, _ := noFollow.Do(r)
	resp.Body.Close()
	if body := get(t, e, resp.Header.Get("Location")); !strings.Contains(body, "Added system Battery bank.") {
		t.Error("our own messages should still show")
	}
}

func TestClientAddressBehindProxies(t *testing.T) {
	cfg, _ := config.Parse([]byte(`{"trusted_proxies": ["172.16.0.0/12"]}`))
	s := &Server{Cfg: cfg}
	cases := []struct{ remote, xff, want string }{
		{"203.0.113.7:5000", "198.51.100.1", "203.0.113.7"},             // not a proxy: header ignored
		{"172.18.0.2:5000", "198.51.100.1", "198.51.100.1"},             // Docker proxy
		{"172.18.0.2:5000", "6.6.6.6, 198.51.100.1", "198.51.100.1"},    // spoofed left entry ignored
		{"172.18.0.2:5000", "198.51.100.1, 172.18.0.3", "198.51.100.1"}, // chain of proxies
		{"127.0.0.1:5000", "198.51.100.1", "127.0.0.1"},                 // loopback isn't trusted here
		{"[2001:db8:1:2:3:4:5:6]:5000", "", "2001:db8:1:2::/64"},        // IPv6 grouped by /64
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := s.clientAddr(r); got != c.want {
			t.Errorf("%s via %q = %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestConfigAccountSessionsEndWithPasswordChange(t *testing.T) {
	e := demoServer(t, []config.User{{Username: "breakglass", Password: "old password 1", Role: config.RoleAdmin}})
	b := newBrowser(t, e)
	if code, _ := b.login("breakglass", "old password 1"); code != http.StatusSeeOther {
		t.Fatal("sign in")
	}
	e.server.Cfg.Auth.Users[0].Password = "new password 2" // edited in the config, then restarted
	if code, _, _ := b.do("GET", "/admin/sites", nil); code != http.StatusSeeOther {
		t.Errorf("old session still works after the config password changed: %d", code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := demoServerWith(t, nil, func(s *Server) { s.Cfg.PublicURL = "https://pm.example.com" })
	resp, err := http.Get(e.srv.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	h := resp.Header
	for k, want := range map[string]string{"X-Frame-Options": "DENY", "X-Content-Type-Options": "nosniff",
		"Strict-Transport-Security": "max-age=31536000", "Cache-Control": "no-store"} {
		if h.Get(k) != want {
			t.Errorf("%s = %q", k, h.Get(k))
		}
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Errorf("CSP = %q", h.Get("Content-Security-Policy"))
	}
	addUser(t, e, "somchai", config.RoleAdmin, "correct horse battery")
	r, _ := http.NewRequest("POST", e.srv.URL+"/login", strings.NewReader("username=somchai&password=correct+horse+battery"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, _ = noFollow.Do(r)
	resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie && !c.Secure {
			t.Error("session cookie should be Secure when public_url is https")
		}
	}
}

func TestLockoutOfUnknownNamesStaysOutOfActivity(t *testing.T) {
	e := demoServer(t, nil)
	b := newBrowser(t, e)
	for i := 0; i < 5; i++ {
		b.login("no-such-user", "guess")
	}
	for _, a := range e.store.Audit(5) {
		if strings.Contains(a.Action, "no-such-user") {
			t.Error("lockouts of made-up usernames shouldn't fill the activity log")
		}
	}
}

// FuzzSafeNext: whatever comes in, the redirect stays a plain path on this site.
func FuzzSafeNext(f *testing.F) {
	for _, s := range []string{"/", "/sites/bkk1?x=1", "//evil", "/\t/evil", `/\evil`, "https://evil", "/%2F%2Fevil", "/\x00", "/ok#frag"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := safeNext(in)
		if !strings.HasPrefix(out, "/") || strings.HasPrefix(out, "//") || strings.ContainsAny(out, "\\\t\r\n\x00") {
			t.Fatalf("safeNext(%q) = %q", in, out)
		}
		u, err := url.Parse(out)
		if err != nil || u.Host != "" || u.Scheme != "" {
			t.Fatalf("safeNext(%q) = %q leaves the site", in, out)
		}
	})
}

// cloudflared on the same machine connects from loopback, which is trusted by
// default. Cloudflare appends the visitor's address to X-Forwarded-For, so a
// value the visitor sent themselves is ignored.
func TestClientAddressViaCloudflared(t *testing.T) {
	cfg, _ := config.Parse([]byte(`{"listen": "127.0.0.1:8080", "public_url": "https://pm.example.com"}`))
	s := &Server{Cfg: cfg}
	for _, c := range []struct{ remote, xff, want string }{
		{"127.0.0.1:41000", "203.0.113.9", "203.0.113.9"},
		{"[::1]:41000", "6.6.6.6, 203.0.113.9", "203.0.113.9"},
		{"127.0.0.1:41000", "", "127.0.0.1"},
	} {
		r := httptest.NewRequest("POST", "/login", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := s.clientAddr(r); got != c.want {
			t.Errorf("%s via %q = %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
	// Form posts arrive with the public origin; they must be accepted.
	r := httptest.NewRequest("POST", "http://127.0.0.1:8080/login", nil)
	r.Host = "127.0.0.1:8080" // in case the tunnel rewrites Host
	r.Header.Set("Origin", "https://pm.example.com")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if !s.sameOrigin(r) {
		t.Error("a post from the public hostname should pass the origin check")
	}
}

func TestNoPasswordsOverPlainHTTP(t *testing.T) {
	e := demoServerWith(t, nil, func(s *Server) {
		s.Cfg.PublicURL = "https://pm.example.com"
		s.Cfg.TrustedProxies = []string{"192.168.50.10"}
	})
	cfg, _ := config.Parse([]byte(`{"public_url": "https://pm.example.com", "trusted_proxies": ["192.168.50.10"], "feed": {"token": "demo"}}`))
	e.server.Cfg = cfg
	e.server.Cfg.Auth.Users = []config.User{{Username: "admin", Password: "pw", Role: config.RoleAdmin}}
	h, err := e.server.Handler()
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path, remote string, form url.Values, hdr ...string) *httptest.ResponseRecorder {
		var r *http.Request
		if form != nil {
			r = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		r.RemoteAddr = remote
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	lan := "192.168.50.77:50000"
	if w := do("GET", "/login", lan, nil); !strings.Contains(w.Body.String(), "isn't encrypted") || strings.Contains(w.Body.String(), `name="password"`) {
		t.Error("the sign-in page opened on the LAN address should send people to the https address instead of showing the form")
	}
	if w := do("POST", "/login", lan, url.Values{"username": {"admin"}, "password": {"pw"}}); w.Code != http.StatusForbidden || len(w.Result().Cookies()) > 0 {
		t.Errorf("a password posted over plain HTTP from the LAN = %d", w.Code)
	}
	r := httptest.NewRequest("GET", "/export.csv", nil)
	r.RemoteAddr = lan
	r.SetBasicAuth("admin", "pw")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "unencrypted") {
		t.Errorf("basic auth over plain HTTP from the LAN = %d", w.Code)
	}
	// Through the tunnel (trusted proxy, or marked https) and from the machine itself: fine.
	for _, c := range []struct {
		remote string
		hdr    []string
	}{{"192.168.50.10:40000", nil}, {"10.1.1.1:40000", []string{"X-Forwarded-Proto", "https"}}, {"127.0.0.1:40000", nil}} {
		if w := do("GET", "/login", c.remote, nil, c.hdr...); !strings.Contains(w.Body.String(), `name="password"`) {
			t.Errorf("sign-in form should show for %s %v", c.remote, c.hdr)
		}
	}
}

func TestRestoreSignsEveryoneOut(t *testing.T) {
	e := demoServer(t, nil)
	addUser(t, e, "mallory", config.RoleAdmin, "mallory password 1")
	m := newBrowser(t, e)
	m.login("mallory", "mallory password 1")
	post(t, e, "/admin/backups", url.Values{})
	files, _ := e.server.Backups.List()
	post(t, e, "/admin/users/mallory/delete", url.Values{})
	msg := post(t, e, "/admin/backups/restore", url.Values{"name": {files[0].Name}, "confirm": {"restore"}})
	if !strings.Contains(msg, "Everyone has been signed out") {
		t.Errorf("restore message: %q", msg)
	}
	if code, _, _ := m.do("GET", "/admin/users", nil); code != http.StatusSeeOther {
		t.Errorf("a deleted account's old sign-in came back after the restore: %d", code)
	}
	if code, _ := newBrowser(t, e).login("mallory", "mallory password 1"); code != http.StatusSeeOther {
		t.Error("the restored account can sign in again with its password")
	}
}
