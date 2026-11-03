package web

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apinat33198/pmtracker/internal/auth"
	"github.com/apinat33198/pmtracker/internal/config"
	"github.com/apinat33198/pmtracker/internal/core"
	"github.com/apinat33198/pmtracker/internal/demo"
	"github.com/apinat33198/pmtracker/internal/gcal"
	"github.com/apinat33198/pmtracker/internal/store"
)

type env struct {
	srv    *httptest.Server
	store  *store.Store
	server *Server
}

func TestMain(m *testing.M) {
	auth.Iterations = 1000 // keep hashing fast in tests
	os.Exit(m.Run())
}

// demoServer serves the demo data. With users nil there is one config
// account, admin (password "pw"); every request helper signs in with "pw".
func demoServer(t *testing.T, users []config.User) env {
	return demoServerWith(t, users, nil)
}

func demoServerWith(t *testing.T, users []config.User, opts func(*Server)) env {
	t.Helper()
	if users == nil {
		users = []config.User{{Username: "admin", Password: "pw", Role: config.RoleAdmin}}
	}
	loc, _ := time.LoadLocation("Asia/Bangkok")
	now := func() time.Time { return time.Date(2026, 11, 3, 9, 0, 0, 0, loc) }
	cfg, err := demo.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Auth.Users = users
	cfg.Auth.AnonymousAccess = config.AnonymousNone
	cat := demo.Catalog(now())
	src := demo.NewSource(cfg, &cat, now())
	st, err := store.Open(filepath.Join(t.TempDir(), "d.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetClock(now)
	if err := st.UpdateCatalog("test", "seed", func(c *core.Catalog) error { *c = cat; return nil }); err != nil {
		t.Fatal(err)
	}
	quiet := log.New(io.Discard, "", 0)
	sy := &gcal.Syncer{Store: st, Source: src, Cfg: cfg, Now: now, Log: quiet}
	sy.SyncAll(context.Background())
	sy.SyncAll(context.Background())
	s := &Server{Cfg: cfg, Store: st, Syncer: sy, Now: now, Log: quiet, Demo: true}
	if opts != nil {
		opts(s)
	}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return env{srv: srv, store: st, server: s}
}

var noFollow = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func req(t *testing.T, method, u, user string, form url.Values) (int, string, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r, _ := http.NewRequest(method, u, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if user != "" {
		r.SetBasicAuth(user, "pw")
	}
	resp, err := noFollow.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	loc := resp.Header.Get("Location")
	if loc != "" {
		if lu, err := url.Parse(loc); err == nil {
			loc = lu.Query().Get("msg")
		}
	}
	return resp.StatusCode, string(b), loc
}

func get(t *testing.T, e env, path string) string {
	t.Helper()
	code, body, _ := req(t, "GET", e.srv.URL+path, "admin", nil)
	if code != 200 {
		t.Fatalf("GET %s = %d: %.300s", path, code, body)
	}
	if strings.Contains(body, "ZgotmplZ") || strings.Contains(body, "<no value>") || strings.Contains(body, "%!") {
		t.Errorf("GET %s: template produced an unsafe or missing value", path)
	}
	return body
}

func post(t *testing.T, e env, path string, form url.Values) string {
	t.Helper()
	code, body, msg := req(t, "POST", e.srv.URL+path, "admin", form)
	if code != http.StatusSeeOther && code != 200 {
		t.Fatalf("POST %s = %d: %.300s", path, code, body)
	}
	return msg
}

func TestPagesRender(t *testing.T) {
	e := demoServer(t, nil)
	cat := e.store.Catalog()
	paths := []string{"/", "/?region=South", "/?vendor=siamups&status=overdue", "/?system=crac", "/inbox", "/events", "/events?show=partial",
		"/export.csv", "/digest.txt", "/forecast.ics?token=demo", "/admin/sites", "/admin/vendors", "/admin/systems",
		"/admin/calendars", "/admin/import", "/admin/activity", "/admin/import/template.csv"}
	for _, s := range cat.Sites {
		paths = append(paths, "/sites/"+s.ID, "/admin/sites/"+s.ID)
	}
	for _, tk := range cat.Tasks {
		paths = append(paths, "/tasks/"+tk.ID, "/admin/tasks/"+tk.ID)
	}
	for _, v := range cat.Vendors {
		paths = append(paths, "/admin/vendors/"+v.Key)
	}
	for _, p := range paths {
		get(t, e, p)
	}
	home := get(t, e, "/")
	if !strings.Contains(home, "PM across 21 sites") || !strings.Contains(home, "Hat Yai") || !strings.Contains(home, "CRAC") {
		t.Error("overview should show all 21 sites and system columns")
	}
	if csv := get(t, e, "/export.csv"); strings.Count(csv, "\n") != len(cat.Tasks)+1 {
		t.Errorf("export should have a header and %d task rows, got %d lines", len(cat.Tasks), strings.Count(csv, "\n"))
	}
}

func TestRoles(t *testing.T) {
	e := demoServer(t, []config.User{
		{Username: "v", Password: "pw", Role: config.RoleViewer},
		{Username: "ed", Password: "pw", Role: config.RoleEditor},
		{Username: "ad", Password: "pw", Role: config.RoleAdmin},
	})
	u := e.srv.URL
	cases := []struct {
		method, path, user string
		want               int
	}{
		{"GET", "/", "", 303},
		{"GET", "/admin/sites", "", 303},
		{"POST", "/inbox/ignore", "", 303},
		{"GET", "/", "nobody", 401},
		{"GET", "/forecast.ics?token=demo", "", 200},
		{"GET", "/forecast.ics?token=wrong", "", 404},
		{"GET", "/login", "", 200},
		{"GET", "/", "v", 200},
		{"GET", "/export.csv", "v", 200},
		{"POST", "/inbox/ignore", "v", 403},
		{"GET", "/admin/sites", "v", 403},
		{"POST", "/inbox/ignore", "ed", 303},
		{"GET", "/admin/sites", "ed", 403},
		{"POST", "/admin/sites", "ed", 403},
		{"GET", "/admin/sites", "ad", 200},
	}
	for _, c := range cases {
		var form url.Values
		if c.method == "POST" {
			form = url.Values{"event": {"facilities@demo|abc-survey"}}
		}
		if code, _, _ := req(t, c.method, u+c.path, c.user, form); code != c.want {
			t.Errorf("%s %s as %q = %d, want %d", c.method, c.path, c.user, code, c.want)
		}
	}
	_, body, _ := req(t, "GET", u+"/", "v", nil)
	if strings.Contains(body, `href="/admin/sites"`) || strings.Contains(body, "Sync now") {
		t.Error("viewers shouldn't see admin links or sync buttons")
	}
	if a := e.store.Audit(5); len(a) == 0 || a[0].By != "ed" {
		t.Errorf("editor's inbox decision should be audited under their name: %+v", a)
	}
}

func TestAdminSiteLifecycle(t *testing.T) {
	e := demoServer(t, nil)

	msg := post(t, e, "/admin/sites", url.Values{"code": {"PTT1"}, "name": {"Pattaya"}, "region": {"East"},
		"vendor_dc": {"voltedge"}, "last_dc": {"2026-09-01"}, "vendor_ups": {"abcpower"}, "vendor_crac": {"coolair"}, "last_crac": {"2026-10-20"}})
	if !strings.Contains(msg, "Added PTT1 Pattaya") {
		t.Fatalf("create: %q", msg)
	}
	if msg := post(t, e, "/admin/sites", url.Values{"code": {"ptt1"}, "name": {"Dup"}}); !strings.Contains(msg, "already in use") {
		t.Errorf("duplicate code: %q", msg)
	}
	cat := e.store.Catalog()
	site, ok := cat.SiteByCode("PTT1")
	if !ok || len(cat.TasksAt(site.ID)) != 3 {
		t.Fatalf("PTT1 should have 3 tasks")
	}
	snap := core.Build(mustCfg(t), e.store.Inputs(), time.Date(2026, 11, 3, 9, 0, 0, 0, time.FixedZone("ICT", 7*3600)))
	ss, _ := snap.Site(site.ID)
	if ss.LastPM.Format("2006-01-02") != "2026-10-20" {
		t.Errorf("site last PM = %s", ss.LastPM.Format("2006-01-02"))
	}

	// The unknown-site event in the demo inbox ("[PM] PTT1 UPS – ABC Power") now matches.
	if strings.Contains(get(t, e, "/inbox"), "[PM] PTT1 UPS – ABC Power") {
		t.Error("adding site PTT1 should resolve its inbox event")
	}

	// Rename: the old code stays as another name.
	post(t, e, "/admin/sites/"+site.ID, url.Values{"code": {"PTY1"}, "name": {"Pattaya Beach"}, "region": {"East"}, "aliases": {""}})
	renamed, _ := e.store.Catalog().Site(site.ID)
	if renamed.Code != "PTY1" || len(renamed.Aliases) != 1 || renamed.Aliases[0] != "PTT1" {
		t.Errorf("renamed = %+v", renamed)
	}

	// Add a second UPS vendor, then edit and delete it.
	post(t, e, "/admin/sites/"+site.ID+"/tasks", url.Values{"system": {"ups"}, "vendor": {"siamups"}, "interval_months": {"6"}, "last_pm": {"2026-06-01"}})
	var tid string
	for _, tk := range e.store.Catalog().TasksAt(site.ID) {
		if tk.Vendor == "siamups" {
			tid = tk.ID
		}
	}
	if tid == "" {
		t.Fatal("second UPS task not added")
	}
	if msg := post(t, e, "/admin/tasks/"+tid, url.Values{"system": {"ups"}, "vendor": {"siamups"}, "interval_months": {"6"}, "anchor_mode": {"fixed"}}); !strings.Contains(msg, "need a first slot date") {
		t.Errorf("fixed without anchor should be refused: %q", msg)
	}
	post(t, e, "/admin/tasks/"+tid, url.Values{"system": {"ups"}, "vendor": {"siamups"}, "interval_months": {"6"}, "anchor_mode": {"fixed"}, "anchor_date": {"2026-01-15"}, "paused": {"on"}})
	if tk, _ := e.store.Catalog().Task(tid); tk.AnchorMode != "fixed" || !tk.Paused || tk.IntervalMonths != 6 {
		t.Errorf("task = %+v", tk)
	}
	if msg := post(t, e, "/admin/tasks/"+tid+"/delete", url.Values{}); !strings.Contains(msg, "Tick the box") {
		t.Errorf("delete without confirm: %q", msg)
	}
	post(t, e, "/admin/tasks/"+tid+"/delete", url.Values{"confirm": {"on"}})
	if _, ok := e.store.Catalog().Task(tid); ok {
		t.Error("task should be deleted")
	}
	for _, m := range e.store.Inputs().Manual {
		for _, x := range m.Tasks {
			if x == tid {
				t.Error("manual visits of a deleted task should go")
			}
		}
	}

	// Vendors in use can't be deleted; unused ones can.
	if msg := post(t, e, "/admin/vendors/delete", url.Values{"key": {"voltedge"}}); !strings.Contains(msg, "still has") {
		t.Errorf("vendor in use: %q", msg)
	}
	post(t, e, "/admin/vendors", url.Values{"name": {"Temp Vendor"}, "domains": {"@temp.example, x@other.example"}})
	tv, ok := e.store.Catalog().FindVendor("Temp Vendor")
	if !ok || strings.Join(tv.Domains, ",") != "temp.example,other.example" {
		t.Errorf("vendor domains should be cleaned: %+v", tv)
	}
	post(t, e, "/admin/vendors/delete", url.Values{"key": {tv.Key}})
	if _, ok := e.store.Catalog().Vendor(tv.Key); ok {
		t.Error("unused vendor should be deleted")
	}

	// Archive hides the site; delete needs the code typed.
	post(t, e, "/admin/sites/"+site.ID+"/archive", nil)
	if strings.Contains(get(t, e, "/"), "Pattaya Beach") {
		t.Error("archived site should be hidden from the overview")
	}
	if msg := post(t, e, "/admin/sites/"+site.ID+"/delete", url.Values{"confirm": {"nope"}}); !strings.Contains(msg, "Type PTY1") {
		t.Errorf("delete without the code: %q", msg)
	}
	post(t, e, "/admin/sites/"+site.ID+"/delete", url.Values{"confirm": {"pty1"}})
	if _, ok := e.store.Catalog().Site(site.ID); ok || len(e.store.Catalog().TasksAt(site.ID)) != 0 {
		t.Error("site and its tasks should be deleted")
	}
	if !strings.Contains(get(t, e, "/admin/activity"), "Deleted site PTY1 Pattaya Beach and its 3 PM tasks") {
		t.Error("activity log should record the deletion")
	}
}

func TestImportAndInbox(t *testing.T) {
	e := demoServer(t, nil)
	csv := "site_code,site_name,region,system,vendor,interval_months,last_pm_date\nKBI1,Krabi,South,UPS,Siam UPS Service,3,01/10/2569\nKBI1,,,Fire suppression,FireSafe Co,12,\n"
	_, body, _ := req(t, "POST", e.srv.URL+"/admin/import", "admin", url.Values{"csv": {csv}, "mode": {"check"}})
	if !strings.Contains(body, "Nothing was saved yet") || !strings.Contains(body, "1 new site") {
		t.Errorf("check result missing: %.500s", body)
	}
	if _, ok := e.store.Catalog().SiteByCode("KBI1"); ok {
		t.Fatal("check must not save")
	}
	_, body, _ = req(t, "POST", e.srv.URL+"/admin/import", "admin", url.Values{"csv": {csv}, "mode": {"apply"}})
	if !strings.Contains(body, "Imported") {
		t.Errorf("apply result missing")
	}
	cat := e.store.Catalog()
	kbi, ok := cat.SiteByCode("KBI1")
	if !ok || len(cat.TasksAt(kbi.ID)) != 2 {
		t.Fatalf("KBI1 should have 2 tasks")
	}
	if _, ok := cat.FindVendor("FireSafe Co"); !ok {
		t.Error("new vendor should be created")
	}

	// Link the Udon Thani series once; every occurrence follows.
	var uthDC string
	for _, tk := range cat.TasksAt("uth1") {
		if tk.System == "dc" {
			uthDC = tk.ID
		}
	}
	if !strings.Contains(get(t, e, "/inbox"), "Quarterly maintenance visit") {
		t.Fatal("series should be in the inbox")
	}
	msg := post(t, e, "/inbox/link", url.Values{"event": {"facilities@demo|voltedge-uth_20261031"}, "tasks": {uthDC}, "remember": {"series"}})
	if !strings.Contains(msg, "links the rest of the series") {
		t.Errorf("link: %q", msg)
	}
	page := get(t, e, "/tasks/"+uthDC)
	if strings.Count(page, "series rule") < 4 || !strings.Contains(page, "Booked") {
		t.Error("all series visits should be linked and the next one booked")
	}
	if strings.Contains(get(t, e, "/inbox"), "Quarterly maintenance visit") {
		t.Error("series should leave the inbox")
	}

	// Cross-site posts are refused.
	r, _ := http.NewRequest("POST", e.srv.URL+"/inbox/ignore", strings.NewReader("event=x"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://evil.example")
	resp, _ := noFollow.Do(r)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site post = %d", resp.StatusCode)
	}
}

func mustCfg(t *testing.T) *config.Config {
	c, err := demo.Config()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestVendorManagement(t *testing.T) {
	e := demoServer(t, nil)
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.FixedZone("ICT", 7*3600))

	// A full vendor record; the email's domain becomes a matching domain.
	post(t, e, "/admin/vendors", url.Values{"name": {"FreshAir Co"}, "contact": {"Khun Mai"}, "phone": {"02 111 2222"},
		"email": {"service@freshair.example"}, "aliases": {"FreshAir"}})
	fa, ok := e.store.Catalog().FindVendor("FreshAir Co")
	if !ok || fa.Phone != "02 111 2222" || fa.Contact != "Khun Mai" || strings.Join(fa.Domains, ",") != "freshair.example" {
		t.Fatalf("vendor = %+v", fa)
	}
	if msg := post(t, e, "/admin/vendors", url.Values{"name": {"freshair co"}}); !strings.Contains(msg, "already a vendor called") {
		t.Errorf("duplicate name: %q", msg)
	}
	page := get(t, e, "/admin/vendors")
	if !strings.Contains(page, "FreshAir Co") || !strings.Contains(page, "UPS at 10 sites") {
		t.Error("vendor list should show new vendors and what each one services")
	}

	// New vendors straight from the add-site form.
	msg := post(t, e, "/admin/sites", url.Values{"code": {"KBI1"}, "name": {"Krabi"}, "region": {"South"},
		"vendor_ups": {newVendorOption}, "newvendor_ups": {"Krabi Power Service"},
		"vendor_crac": {newVendorOption}, "newvendor_crac": {"freshair"}}) // matches FreshAir by its other name
	if !strings.Contains(msg, "Added KBI1") {
		t.Fatalf("add site: %q", msg)
	}
	cat := e.store.Catalog()
	kbi, _ := cat.SiteByCode("KBI1")
	got := map[string]string{}
	for _, tk := range cat.TasksAt(kbi.ID) {
		got[tk.System] = cat.VendorName(tk.Vendor)
	}
	if got["ups"] != "Krabi Power Service" || got["crac"] != "FreshAir Co" || len(cat.Vendors) != 9 {
		t.Errorf("tasks = %v, vendors = %d (one new, one reused)", got, len(cat.Vendors))
	}

	// New vendor from the add-task form; an empty name is refused.
	if msg := post(t, e, "/admin/sites/"+kbi.ID+"/tasks", url.Values{"system": {"dc"}, "vendor": {newVendorOption}, "new_vendor": {""}}); !strings.Contains(msg, "Enter the new vendor's name") {
		t.Errorf("empty new vendor: %q", msg)
	}
	post(t, e, "/admin/sites/"+kbi.ID+"/tasks", url.Values{"system": {"dc"}, "vendor": {newVendorOption}, "new_vendor": {"Southern DC Ltd"}})
	if _, ok := e.store.Catalog().FindVendor("Southern DC Ltd"); !ok {
		t.Error("add-task should create the new vendor")
	}
	if site := get(t, e, "/sites/"+kbi.ID); !strings.Contains(site, "Vendors at this site") || !strings.Contains(site, "02 111 2222") {
		t.Error("site page should list its vendors with contact details")
	}

	// Moving tasks: refused when it would duplicate a task (BKK2 has UPS by both vendors).
	if msg := post(t, e, "/admin/vendors/abcpower/replace", url.Values{"to": {"siamups"}}); !strings.Contains(msg, "would duplicate") {
		t.Errorf("replace clash: %q", msg)
	}
	// Move every CoolAir task to FreshAir and delete CoolAir; history stays.
	before := core.Build(mustCfg(t), e.store.Inputs(), now)
	var bkk1Crac string
	for _, tk := range e.store.Catalog().TasksAt("bkk1") {
		if tk.System == "crac" {
			bkk1Crac = tk.ID
		}
	}
	fBefore, _ := before.Forecast(bkk1Crac)
	msg = post(t, e, "/admin/vendors/coolair/replace", url.Values{"to": {fa.Key}, "delete_after": {"on"}})
	if !strings.Contains(msg, "Moved 10 PM tasks from CoolAir Services to FreshAir Co") || !strings.Contains(msg, "was deleted") {
		t.Errorf("replace: %q", msg)
	}
	if _, ok := e.store.Catalog().Vendor("coolair"); ok {
		t.Error("CoolAir should be deleted")
	}
	after := core.Build(mustCfg(t), e.store.Inputs(), now)
	fAfter, _ := after.Forecast(bkk1Crac)
	if fAfter.Task.Vendor != fa.Key || fAfter.LastDone == nil || fBefore.LastDone == nil || !fAfter.LastDone.Date.Equal(fBefore.LastDone.Date) {
		t.Errorf("BKK1 CRAC should keep its last PM after the vendor change")
	}

	// Vendors with tasks can't be deleted.
	if msg := post(t, e, "/admin/vendors/delete", url.Values{"key": {fa.Key}}); !strings.Contains(msg, "Move them to another vendor first") {
		t.Errorf("delete in use: %q", msg)
	}
	if !strings.Contains(get(t, e, "/admin/activity"), "Moved the PM tasks of CoolAir Services to FreshAir Co and deleted CoolAir Services") {
		t.Error("activity log should record the move")
	}
}
