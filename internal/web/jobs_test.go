package web

import (
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/apinat33198/pmtracker/internal/config"
)

// matrix is the PM grid's HTML on the overview.
func matrix(t *testing.T, e env) string {
	t.Helper()
	home := get(t, e, "/")
	i, j := strings.Index(home, `<table class="matrix">`), strings.Index(home, `<p class="legend-line">`)
	if i < 0 || j < i {
		t.Fatal("overview has no PM grid")
	}
	return home[i:j]
}

func TestJobsFlow(t *testing.T) {
	e := demoServer(t, nil)
	before := matrix(t, e)
	if home := get(t, e, "/"); strings.Contains(home, "overview-jobs") {
		t.Error("with no open jobs the overview has no jobs section")
	}

	add := url.Values{"site": {"bkk1"}, "title": {"Repair raised floor"}, "date": {"2026-11-03"}, "who": {"Siam Floor Works"},
		"note": {"Quote QT-118"}, "back": {"/sites/bkk1"}}
	if msg := post(t, e, "/jobs", add); msg != `Added “Repair raised floor” at BKK1.` {
		t.Errorf("add = %q", msg)
	}
	jobs := e.store.Inputs().Jobs
	if len(jobs) != 1 || jobs[0].By != "admin" || jobs[0].Who != "Siam Floor Works" {
		t.Fatalf("stored = %+v", jobs)
	}
	id := strconv.Itoa(jobs[0].ID)

	site := get(t, e, "/sites/bkk1")
	if !strings.Contains(site, "Repair raised floor") || !strings.Contains(site, "Quote QT-118") || !strings.Contains(site, `action="/jobs/`+id+`/done"`) {
		t.Error("the site page should list the job with a Done button")
	}
	home := get(t, e, "/")
	if !strings.Contains(home, "overview-jobs") || !strings.Contains(home, "Repair raised floor") || !strings.Contains(home, `Jobs <span class="count">1</span>`) {
		t.Error("the overview should list the open job and count it in the menu")
	}
	if strings.Contains(home[strings.Index(home, "overview-jobs"):], `action="/jobs/`) {
		t.Error("the overview lists jobs without buttons")
	}
	if got := matrix(t, e); got != before {
		t.Error("adding a job must not change the PM grid")
	}
	if d := get(t, e, "/digest.txt"); !strings.Contains(d, "• BKK1 Repair raised floor: today (Siam Floor Works)") {
		t.Errorf("digest should list the job due today:\n%s", d)
	}

	if msg := post(t, e, "/jobs/"+id+"/done", url.Values{}); msg != `Marked “Repair raised floor” at BKK1 as done.` {
		t.Errorf("done = %q", msg)
	}
	if j := e.store.Inputs().Jobs[0]; j.Status != "done" || j.DoneOn != "2026-11-03" || j.DoneBy != "admin" {
		t.Errorf("after done: %+v", j)
	}
	if msg := post(t, e, "/jobs/"+id+"/done", url.Values{}); !strings.Contains(msg, "already done") {
		t.Errorf("done twice = %q", msg)
	}
	if strings.Contains(get(t, e, "/"), "overview-jobs") {
		t.Error("done jobs leave the overview")
	}
	if p := get(t, e, "/jobs"); !strings.Contains(p, "done by admin") {
		t.Error("the jobs page should list it as done")
	}
	if msg := post(t, e, "/jobs/"+id, url.Values{"title": {"Repair raised floor"}, "date": {"2026-11-03"}, "done_on": {"2026-11-04"}}); !strings.Contains(msg, "future") {
		t.Errorf("a done date tomorrow = %q", msg)
	}
	if msg := post(t, e, "/jobs/"+id, url.Values{"title": {"Repair raised floor, row C"}, "date": {"2026-11-02"}, "done_on": {"2026-11-03"}, "who": {""}}); !strings.HasPrefix(msg, "Saved") {
		t.Errorf("edit = %q", msg)
	}
	if j := e.store.Inputs().Jobs[0]; j.Title != "Repair raised floor, row C" || j.Date != "2026-11-02" || j.Who != "" || j.SiteID != "bkk1" {
		t.Errorf("after edit: %+v", j)
	}
	if msg := post(t, e, "/jobs/"+id+"/reopen", url.Values{}); !strings.HasPrefix(msg, "Reopened") {
		t.Errorf("reopen = %q", msg)
	}
	if j := e.store.Inputs().Jobs[0]; j.Status != "open" || j.DoneOn != "" {
		t.Errorf("after reopen: %+v", j)
	}
	if msg := post(t, e, "/jobs/"+id+"/delete", url.Values{}); !strings.HasPrefix(msg, "Deleted") {
		t.Errorf("delete = %q", msg)
	}
	if msg := post(t, e, "/jobs/"+id+"/delete", url.Values{}); msg != "That job no longer exists." {
		t.Errorf("delete twice = %q", msg)
	}
	if got := matrix(t, e); got != before {
		t.Error("the PM grid should be unchanged after all that")
	}

	var acts []string
	for _, a := range e.store.Audit(20) {
		acts = append(acts, a.Action)
	}
	log := strings.Join(acts, "\n")
	for _, w := range []string{`Added the ad-hoc job “Repair raised floor” at BKK1 for 3 Nov 2026`, `Marked as done the ad-hoc job`, `Reopened the ad-hoc job`, `Deleted the ad-hoc job “Repair raised floor, row C” at BKK1`} {
		if !strings.Contains(log, w) {
			t.Errorf("activity log is missing %q:\n%s", w, log)
		}
	}
}

func TestJobsRefuseBadInput(t *testing.T) {
	e := demoServer(t, nil)
	cases := []struct {
		form url.Values
		want string
	}{
		{url.Values{"site": {"nowhere"}, "title": {"x"}, "date": {"2026-11-03"}}, "Pick the site"},
		{url.Values{"site": {"bkk1"}, "title": {"  "}, "date": {"2026-11-03"}}, "enter what the job is"},
		{url.Values{"site": {"bkk1"}, "title": {"x"}, "date": {"tomorrow"}}, "enter the job's date"},
		{url.Values{"site": {"bkk1"}, "title": {strings.Repeat("x", 201)}, "date": {"2026-11-03"}}, "under 200"},
	}
	for _, c := range cases {
		if msg := post(t, e, "/jobs", c.form); !strings.Contains(msg, c.want) {
			t.Errorf("%v = %q, want %q", c.form, msg, c.want)
		}
	}
	if n := len(e.store.Inputs().Jobs); n != 0 {
		t.Errorf("%d jobs were saved", n)
	}
	for _, p := range []string{"/jobs/0/done", "/jobs/abc/done", "/jobs/77/reopen", "/jobs/77"} {
		if msg := post(t, e, p, url.Values{"title": {"x"}, "date": {"2026-11-03"}}); msg != "That job no longer exists." {
			t.Errorf("%s = %q", p, msg)
		}
	}

	// Text is shown as text, and can't run as a formula in a spreadsheet.
	post(t, e, "/jobs", url.Values{"site": {"bkk1"}, "title": {`<script>alert(1)</script>`}, "date": {"2026-11-03"}, "who": {"=HYPERLINK(\"http://x\")"}})
	if p := get(t, e, "/jobs"); strings.Contains(p, "<script>alert") || !strings.Contains(p, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("job titles must be escaped")
	}
	if csv := get(t, e, "/jobs.csv"); !strings.Contains(csv, `'=HYPERLINK`) || strings.Count(csv, "\n") != 2 {
		t.Errorf("csv = %s", csv)
	}
}

func TestJobsRoles(t *testing.T) {
	e := demoServer(t, []config.User{
		{Username: "v", Password: "pw", Role: config.RoleViewer},
		{Username: "ed", Password: "pw", Role: config.RoleEditor},
	})
	u := e.srv.URL
	form := url.Values{"site": {"bkk1"}, "title": {"Repair raised floor"}, "date": {"2026-11-03"}}
	if code, _, _ := req(t, "POST", u+"/jobs", "v", form); code != 403 {
		t.Errorf("viewer adding a job = %d", code)
	}
	if code, _, _ := req(t, "POST", u+"/jobs", "", form); code != 303 || len(e.store.Inputs().Jobs) != 0 {
		t.Errorf("signed out adding a job = %d", code)
	}
	if code, _, _ := req(t, "POST", u+"/jobs", "ed", form); code != 303 || len(e.store.Inputs().Jobs) != 1 {
		t.Errorf("editor adding a job = %d", code)
	}
	id := strconv.Itoa(e.store.Inputs().Jobs[0].ID)
	for _, p := range []string{"/jobs/" + id, "/jobs/" + id + "/done", "/jobs/" + id + "/delete"} {
		if code, _, _ := req(t, "POST", u+p, "v", form); code != 403 {
			t.Errorf("viewer POST %s = %d", p, code)
		}
	}
	code, body, _ := req(t, "GET", u+"/jobs", "v", nil)
	if code != 200 || !strings.Contains(body, "Repair raised floor") || strings.Contains(body, `action="/jobs`) {
		t.Errorf("viewers see jobs without forms (%d)", code)
	}
	if code, _, _ := req(t, "GET", u+"/jobs.csv", "v", nil); code != 200 {
		t.Errorf("viewer CSV = %d", code)
	}
	if _, body, _ := req(t, "GET", u+"/sites/bkk1", "v", nil); strings.Contains(body, `action="/jobs`) {
		t.Error("viewers get no job forms on the site page")
	}
}

// Found in testing: a job title with quotes was shown with backslashes, and
// line breaks in names reached the activity log (and its server-log copy)
// as extra lines.
func TestJobAndNameTextStaysOneLine(t *testing.T) {
	e := demoServer(t, nil)
	if msg := post(t, e, "/jobs", url.Values{"site": {"bkk1"}, "title": {`Fix "A" door`}, "date": {"2026-11-03"}}); msg != `Added “Fix "A" door” at BKK1.` {
		t.Errorf("quotes = %q", msg)
	}
	for _, c := range []url.Values{
		{"site": {"bkk1"}, "title": {"\u200b \u200b"}, "date": {"2026-11-03"}},
		{"site": {"bkk1"}, "title": {"x"}, "date": {"0026-11-03"}},
	} {
		if msg := post(t, e, "/jobs", c); !strings.Contains(msg, "wasn't added") {
			t.Errorf("%q = %q, want refused", c, msg)
		}
	}
	forged := "Battery\n2026/11/04 19:00:00 activity: somchai: Deleted all backups"
	post(t, e, "/admin/systems", url.Values{"name": {forged}, "aliases": {"Batt\r\nery, \u202egnp.exe"}})
	sys, ok := e.store.Catalog().FindSystem("Battery 2026/11/04 19:00:00 activity: somchai: Deleted all backups")
	if !ok || strings.ContainsAny(sys.Name, "\r\n") || strings.Join(sys.Aliases, "|") != "Batt|ery|gnp.exe" {
		t.Errorf("system saved as %+v (%v)", sys, ok)
	}
	for _, a := range e.store.Audit(10) {
		if strings.ContainsAny(a.Action+a.By+a.From, "\r\n\u202e") {
			t.Errorf("activity entry spans lines: %q", a.Action)
		}
	}
	code, _, _ := req(t, "GET", e.srv.URL+"/forecast.ics?token=demo", "", nil)
	r, _ := noFollow.Get(e.srv.URL + "/forecast.ics?token=demo")
	if code != 200 || !strings.Contains(r.Header.Get("Cache-Control"), "no-store") {
		t.Errorf("feed = %d, Cache-Control %q", code, r.Header.Get("Cache-Control"))
	}
	r.Body.Close()
}
