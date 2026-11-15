package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/apinat33198/pmtracker/internal/config"
	"github.com/apinat33198/pmtracker/internal/core"
	"github.com/apinat33198/pmtracker/internal/demo"
)

// findingValues is a filled-in finding form for BKK1.
func findingValues(mod func(url.Values)) url.Values {
	v := url.Values{"site": {"bkk1"}, "found_on": {"2026-11-01"}, "kind": {"equipment"}, "severity": {"high"},
		"title": {"Battery string 2 over the resistance limit"}, "reading": {"6.2 mΩ, limit 5.5"}, "owner": {"ABC Power"},
		"due": {"2026-11-02"}, "status": {"open"}}
	if mod != nil {
		mod(v)
	}
	return v
}

func TestFindingsFlow(t *testing.T) {
	e := demoServer(t, nil)
	before := matrix(t, e)
	snap := e.server.snapshot()

	// "Log finding" on a past visit fills in its date, site and task.
	var visit core.Visit
	for _, v := range snap.Visits {
		if v.Status.Counts() && len(v.Tasks) == 1 {
			visit = v
			break
		}
	}
	task, _ := snap.Catalog.Task(visit.Tasks[0])
	form := get(t, e, "/findings/new?visit="+url.QueryEscape(visit.ID))
	if !strings.Contains(form, `value="`+visit.Date.Format("2006-01-02")+`"`) || !strings.Contains(form, `value="`+task.SiteID+`"`) ||
		!strings.Contains(form, `<option value="`+task.ID+`" selected>`) || !strings.Contains(form, "From the PM visit on") {
		t.Error("the form should be filled in from the visit")
	}
	if site := get(t, e, "/tasks/"+task.ID); !strings.Contains(site, `href="/findings/new?visit=`) {
		t.Error("past visits should have a Log finding link")
	}
	if p := get(t, e, "/findings/new"); !strings.Contains(p, "Pick a site") {
		t.Error("a new finding with no site starts by picking one")
	}

	ups := ""
	for _, tk := range snap.Catalog.TasksAt("bkk1") {
		if tk.System == "ups" {
			ups = tk.ID
		}
	}
	if msg := post(t, e, "/findings", findingValues(func(v url.Values) { v.Set("task", ups) })); !strings.HasPrefix(msg, "Logged finding #") {
		t.Fatalf("add = %q", msg)
	}
	fs := e.store.Inputs().Findings
	if len(fs) != 1 || fs[0].By != "admin" || fs[0].TaskID != ups || fs[0].Status != core.FindingOpen || fs[0].Reading != "6.2 mΩ, limit 5.5" {
		t.Fatalf("stored = %+v", fs)
	}
	id := strconv.Itoa(fs[0].ID)

	home := get(t, e, "/")
	if !strings.Contains(home, `Findings <span class="count">1</span>`) {
		t.Error("the menu should count open findings")
	}
	if got := matrix(t, e); got != before {
		t.Error("a finding must not change the PM grid")
	}
	list := get(t, e, "/findings")
	if !strings.Contains(list, "Battery string 2 over the resistance limit") || !strings.Contains(list, "Past target") {
		t.Error("the findings page should list it as past its target")
	}
	for _, p := range []string{"/sites/bkk1", "/tasks/" + ups, "/findings/" + id} {
		if !strings.Contains(get(t, e, p), "Battery string 2 over the resistance limit") {
			t.Errorf("%s should show the finding", p)
		}
	}
	if d := get(t, e, "/digest.txt"); !strings.Contains(d, "Findings past their target date (1)\n• BKK1 #"+id+" Battery string 2 over the resistance limit (high): 1 day late (ABC Power)") {
		t.Errorf("digest:\n%s", d)
	}

	// Closing needs what was done; the form comes back with what was typed.
	edit := findingValues(func(v url.Values) {
		v.Set("task", ups)
		v.Set("status", "closed")
		v.Set("cause", "Cells past their life")
	})
	code, body, _ := req(t, "POST", e.srv.URL+"/findings/"+id, "admin", edit)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "enter what was done before closing it") || !strings.Contains(body, "Cells past their life") {
		t.Errorf("closing with no action = %d", code)
	}
	if e.store.Inputs().Findings[0].Status != core.FindingOpen {
		t.Error("a refused change must not be saved")
	}
	edit.Set("action", "String replaced by ABC Power")
	if msg := post(t, e, "/findings/"+id, edit); msg != "Saved finding #"+id+"." {
		t.Errorf("close = %q", msg)
	}
	f := e.store.Inputs().Findings[0]
	if f.Status != core.FindingClosed || f.ClosedOn != "2026-11-03" || f.ClosedBy != "admin" || f.Cause != "Cells past their life" || f.SiteID != "bkk1" {
		t.Errorf("after closing: %+v", f)
	}

	// It comes back: "Log this coming back" links the new one to it.
	again := get(t, e, "/findings/new?repeat="+id)
	if !strings.Contains(again, `<option value="`+id+`" selected>`) || !strings.Contains(again, "A repeat of finding #"+id) {
		t.Error("the repeat form should be linked to the earlier finding")
	}
	if msg := post(t, e, "/findings", findingValues(func(v url.Values) {
		v.Set("task", ups)
		v.Set("found_on", "2026-11-03")
		v.Set("repeat_of", id)
		v.Set("spread", "todo")
		v.Del("due")
	})); !strings.HasPrefix(msg, "Logged finding #") {
		t.Fatalf("repeat = %q", msg)
	}
	second := e.store.Inputs().Findings[1]
	page := get(t, e, "/findings/"+id)
	if second.RepeatOf != fs[0].ID || !strings.Contains(page, "Came back") || !strings.Contains(page, "came back 1×") {
		t.Error("the earlier finding should show it came back")
	}
	if l := get(t, e, "/findings?status=all"); !strings.Contains(l, `<dd class="kpi">50%</dd>`) {
		t.Error("one of two findings is a repeat: 50%")
	}

	// Refused: dates in the future, a repeat at another site, a task elsewhere.
	cnxTask := snap.Catalog.TasksAt("cnx1")[0].ID
	for _, c := range []struct {
		form url.Values
		want string
	}{
		{findingValues(func(v url.Values) { v.Set("found_on", "2026-11-04") }), "be in the future"},
		{findingValues(func(v url.Values) { v.Set("task", cnxTask) }), "PM tasks, or none"},
		{findingValues(func(v url.Values) { v.Set("site", "cnx1"); v.Set("repeat_of", id) }), "at another site"},
		{findingValues(func(v url.Values) { v.Set("severity", "") }), "how serious"},
		{findingValues(func(v url.Values) { v.Set("repeat_of", "abc") }), "pick the earlier finding"},
	} {
		code, body, _ := req(t, "POST", e.srv.URL+"/findings", "admin", c.form)
		if code != http.StatusUnprocessableEntity || !strings.Contains(body, c.want) {
			t.Errorf("%v = %d, want %q", c.form, code, c.want)
		}
	}
	if msg := post(t, e, "/findings", findingValues(func(v url.Values) { v.Set("site", "old") })); msg != "Pick the site the finding is at." {
		t.Errorf("unknown site = %q", msg)
	}
	if n := len(e.store.Inputs().Findings); n != 2 {
		t.Errorf("%d findings saved", n)
	}

	// Deleting the first keeps the repeat, without the link.
	if msg := post(t, e, "/findings/"+id+"/delete", url.Values{}); msg != "Deleted finding #"+id+"." {
		t.Errorf("delete = %q", msg)
	}
	if fs := e.store.Inputs().Findings; len(fs) != 1 || fs[0].RepeatOf != 0 {
		t.Errorf("after delete: %+v", fs)
	}
	if msg := post(t, e, "/findings/"+id+"/delete", url.Values{}); msg != "That finding no longer exists." {
		t.Errorf("delete twice = %q", msg)
	}
	if code, _, _ := req(t, "GET", e.srv.URL+"/findings/"+id, "admin", nil); code != 404 {
		t.Errorf("a deleted finding's page = %d", code)
	}
	if got := matrix(t, e); got != before {
		t.Error("the PM grid should be unchanged after all that")
	}

	var acts []string
	for _, a := range e.store.Audit(20) {
		acts = append(acts, a.Action)
	}
	log := strings.Join(acts, "\n")
	for _, w := range []string{
		"Logged finding #" + id + " at BKK1 (UPS, high): “Battery string 2 over the resistance limit”",
		"Closed finding #" + id + " at BKK1 (UPS, high)",
		"Deleted finding #" + id + " at BKK1",
	} {
		if !strings.Contains(log, w) {
			t.Errorf("activity log is missing %q:\n%s", w, log)
		}
	}
}

func TestFindingsRoles(t *testing.T) {
	e := demoServer(t, []config.User{
		{Username: "v", Password: "pw", Role: config.RoleViewer},
		{Username: "ed", Password: "pw", Role: config.RoleEditor},
		{Username: "ad", Password: "pw", Role: config.RoleAdmin},
	})
	u := e.srv.URL
	form := findingValues(nil)
	if code, _, _ := req(t, "POST", u+"/findings", "v", form); code != 403 {
		t.Errorf("viewer adding a finding = %d", code)
	}
	if code, _, _ := req(t, "POST", u+"/findings", "", form); code != 303 || len(e.store.Inputs().Findings) != 0 {
		t.Errorf("signed out adding a finding = %d", code)
	}
	if code, _, _ := req(t, "POST", u+"/findings", "ed", form); code != 303 || len(e.store.Inputs().Findings) != 1 {
		t.Errorf("editor adding a finding = %d", code)
	}
	id := strconv.Itoa(e.store.Inputs().Findings[0].ID)
	for _, c := range []struct {
		method, path, user string
		want               int
	}{
		{"GET", "/findings", "v", 200},
		{"GET", "/findings/" + id, "v", 200},
		{"GET", "/findings.csv", "v", 200},
		{"GET", "/findings/new", "v", 403},
		{"POST", "/findings/" + id, "v", 403},
		{"POST", "/findings/" + id + "/delete", "v", 403},
		{"POST", "/findings/" + id + "/delete", "ed", 403}, // findings are evidence: admins only
		{"POST", "/findings/" + id, "ed", 303},
	} {
		if code, _, _ := req(t, c.method, u+c.path, c.user, form); code != c.want {
			t.Errorf("%s %s as %s = %d, want %d", c.method, c.path, c.user, code, c.want)
		}
	}
	_, body, _ := req(t, "GET", u+"/findings/"+id, "v", nil)
	if !strings.Contains(body, "Battery string 2") || strings.Contains(body, `action="/findings`) || strings.Contains(body, "Log a finding") {
		t.Error("viewers see findings without forms")
	}
	if _, body, _ := req(t, "GET", u+"/findings/"+id, "ed", nil); !strings.Contains(body, `action="/findings/`+id+`"`) || strings.Contains(body, "/delete") {
		t.Error("editors get the edit form but no delete")
	}
	if _, body, _ := req(t, "GET", u+"/sites/bkk1", "v", nil); strings.Contains(body, "/findings/new") {
		t.Error("viewers get no Log finding links on the site page")
	}
	if code, _, _ := req(t, "POST", u+"/findings/"+id+"/delete", "ad", url.Values{}); code != 303 || len(e.store.Inputs().Findings) != 0 {
		t.Errorf("admin delete = %d", code)
	}
}

func TestFindingTextIsSafe(t *testing.T) {
	e := demoServer(t, nil)
	post(t, e, "/findings", findingValues(func(v url.Values) {
		v.Set("title", `<script>alert(1)</script>`)
		v.Set("owner", "=HYPERLINK(\"http://x\")")
		v.Set("detail", "line one\r\nline two")
	}))
	id := strconv.Itoa(e.store.Inputs().Findings[0].ID)
	for _, p := range []string{"/findings", "/findings/" + id, "/sites/bkk1"} {
		if b := get(t, e, p); strings.Contains(b, "<script>alert") || !strings.Contains(b, "&lt;script&gt;alert(1)&lt;/script&gt;") {
			t.Errorf("%s: finding titles must be escaped", p)
		}
	}
	if csv := get(t, e, "/findings.csv"); !strings.Contains(csv, `'=HYPERLINK`) || !strings.HasPrefix(csv, "\ufeffid,site_code") {
		t.Errorf("csv = %s", csv)
	}
	for _, a := range e.store.Audit(5) {
		if strings.ContainsAny(a.Action, "\r\n") {
			t.Errorf("activity entry spans lines: %q", a.Action)
		}
	}
}

// The demo's findings are valid and every page shows them.
func TestDemoFindings(t *testing.T) {
	e := demoServer(t, nil)
	logged := len(e.store.Audit(100))
	err := demo.SeedFindings(e.store.Catalog(), e.server.Now(), e.server.Cfg.Location(), func(f core.Finding) (core.Finding, error) {
		return e.store.AddFinding(f, func(core.Finding) string { return "" })
	})
	if err != nil {
		t.Fatal(err)
	}
	snap := e.server.snapshot()
	if len(snap.Findings) != 10 || snap.OpenFindings != 5 {
		t.Errorf("demo has %d findings, %d open", len(snap.Findings), snap.OpenFindings)
	}
	paths := []string{"/findings", "/findings?status=all", "/findings?status=closed&severity=high", "/findings?kind=5s", "/findings.csv"}
	for _, f := range snap.Findings {
		paths = append(paths, "/findings/"+strconv.Itoa(f.ID), "/findings?site="+f.SiteID+"&status=all", "/sites/"+f.SiteID, "/findings/new?repeat="+strconv.Itoa(f.ID))
		if f.HasTask {
			paths = append(paths, "/tasks/"+f.Task.ID)
		}
	}
	for _, p := range paths {
		get(t, e, p)
	}
	// The CNX1 alarm came back twice: its page shows all three.
	for _, f := range snap.Findings {
		if f.Title == "CRAC 1 high-pressure alarm" {
			if p := get(t, e, "/findings/"+strconv.Itoa(f.ID)); strings.Count(p, "high-pressure alarm") < 3 {
				t.Error("the first CNX1 alarm should show both times it came back")
			}
		}
	}
	if len(e.store.Audit(100)) != logged {
		t.Error("seeding the demo shouldn't fill the activity log")
	}
}
