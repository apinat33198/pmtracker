package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/apinat33198/pmtracker/internal/config"
)

func TestActivityRecordsSignIns(t *testing.T) {
	e := demoServer(t, []config.User{{Username: "admin", Password: "pw", Role: config.RoleAdmin},
		{Username: "ed", Password: "pw", Role: config.RoleEditor}})
	addUser(t, e, "somchai", config.RoleAdmin, "correct horse battery")
	b := newBrowser(t, e)
	b.login("somchai", "wrong password!")
	b.login("nobody", "whatever123")
	b.login("somchai", "correct horse battery")
	b.do("POST", "/logout", url.Values{})
	var got []string
	for _, a := range e.store.Audit(10) {
		got = append(got, a.By+"|"+a.From+"|"+a.Action)
	}
	want := []string{"somchai|127.0.0.1|Signed out", "somchai|127.0.0.1|Signed in", "somchai|127.0.0.1|Sign-in failed: wrong password"}
	if len(got) < 3 || strings.Join(got[:3], "\n") != strings.Join(want, "\n") {
		t.Errorf("activity =\n%s", strings.Join(got, "\n"))
	}
	for _, g := range got {
		if strings.Contains(g, "nobody") {
			t.Error("made-up usernames stay out of the activity log")
		}
	}

	// The page shows the retention and where entries came from.
	body := get(t, e, "/admin/activity")
	if !strings.Contains(body, "kept for 90 days") || !strings.Contains(body, "<code>127.0.0.1</code>") {
		t.Errorf("activity page: %.400s", body[strings.Index(body, "<main>"):])
	}

	// CSV export: admins only, everything kept, safe to open in Excel, and logged.
	post(t, e, "/admin/systems", url.Values{"name": {"=HYPERLINK(1)"}})
	code, csv, _ := req(t, "GET", e.srv.URL+"/admin/activity.csv", "admin", nil)
	if code != 200 || !strings.HasPrefix(strings.TrimPrefix(csv, "\ufeff"), "time,by,from,action\n") ||
		!strings.Contains(csv, "somchai,127.0.0.1,Signed in") || !strings.Contains(csv, "'=HYPERLINK") && !strings.Contains(csv, "Added system =HYPERLINK") {
		t.Errorf("export = %d: %.500s", code, csv)
	}
	if strings.Contains(csv, ",=HYPERLINK") {
		t.Error("a cell starting with = must be escaped")
	}
	if a := e.store.Audit(1); a[0].Action != "Downloaded the activity log" {
		t.Errorf("export not logged: %+v", a[0])
	}
	if code, _, _ := req(t, "GET", e.srv.URL+"/admin/activity.csv", "ed", nil); code != http.StatusForbidden {
		t.Errorf("editor export = %d", code)
	}
}
