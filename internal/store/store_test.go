package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apinat33198/pmtracker/internal/auth"
	"github.com/apinat33198/pmtracker/internal/core"
)

func day(d string) time.Time { x, _ := time.Parse("2006-01-02", d); return x }

func TestApplySync(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := day("2026-11-03")
	if n, err := s.ApplySync("cal", true, []core.Event{{ID: "x", Status: "confirmed", Start: now}}, "tok0", now); err != nil || n != 0 || len(s.Inputs().Events) != 0 {
		t.Fatal("a calendar that isn't tracked (removed mid-sync) must not be written")
	}
	if err := s.UpdateCatalog("t", "add", func(c *core.Catalog) error {
		c.Calendars = []core.Calendar{{ID: "cal", Label: "PM"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	full := []core.Event{
		{ID: "a", Status: "confirmed", Summary: "[PM] BKK1 UPS", Start: day("2026-11-20"), AllDay: true},
		{ID: "b", Status: "confirmed", Summary: "[PM] BKK1 DC", Start: day("2026-12-01"), AllDay: true},
	}
	if _, err := s.ApplySync("cal", true, full, "tok1", now); err != nil {
		t.Fatal(err)
	}
	inc := []core.Event{
		{ID: "a", Status: "confirmed", Summary: "[PM] BKK1 UPS", Start: day("2026-11-25"), AllDay: true},
		{ID: "b", Status: "cancelled"},
		{ID: "ghost", Status: "cancelled"},
	}
	if n, err := s.ApplySync("cal", false, inc, "tok2", now); err != nil || n != 2 {
		t.Fatalf("changes = %d, %v", n, err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	in := s2.Inputs()
	if len(in.Events) != 2 || len(in.Changes) != 2 || in.Changes[0].Old != "2026-11-20" {
		t.Fatalf("events %d, changes %+v", len(in.Events), in.Changes)
	}
	if e, _ := s2.Event("cal|b"); e.Status != "cancelled" || e.Summary != "[PM] BKK1 DC" {
		t.Errorf("cancelled event keeps its fields: %+v", e)
	}
	// A later full sync replaces the mirror: events it doesn't list are dropped.
	if n, err := s2.ApplySync("cal", true, []core.Event{full[0]}, "tok3", now); err != nil || n == 0 || len(s2.Inputs().Events) != 1 {
		t.Errorf("full resync = %d, %v, %d events", n, err, len(s2.Inputs().Events))
	}
}

func TestUpdateCatalogCleansUp(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "d.json"))
	s.SetClock(func() time.Time { return day("2026-11-03") })
	err := s.UpdateCatalog("admin", "Set up", func(c *core.Catalog) error {
		c.Sites = []core.Site{{ID: "bkk1", Code: "BKK1", Name: "Bang Na"}}
		c.Systems = core.DefaultSystems()
		c.Vendors = []core.Vendor{{Key: "abc", Name: "ABC Power"}}
		c.Calendars = []core.Calendar{{ID: "cal", Label: "PM"}}
		c.AddTask(core.Task{SiteID: "bkk1", System: "ups", Vendor: "abc"})
		c.AddTask(core.Task{SiteID: "bkk1", System: "dc", Vendor: "abc"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A bad edit is refused and nothing changes.
	err = s.UpdateCatalog("admin", "Bad", func(c *core.Catalog) error {
		c.Tasks = append(c.Tasks, core.Task{ID: "t9", SiteID: "nope", System: "ups", Vendor: "abc"})
		return nil
	})
	if err == nil || len(s.Catalog().Tasks) != 2 {
		t.Fatalf("invalid edit should be refused: %v", err)
	}

	s.ApplySync("cal", true, []core.Event{{ID: "e", Status: "confirmed", Start: day("2026-10-01")}}, "tok", day("2026-11-03"))
	s.AddManual("Added", core.ManualVisit{Date: "2026-09-01", Tasks: []string{"t1"}}, core.ManualVisit{Date: "2026-09-02", Tasks: []string{"t1", "t2"}})
	s.Decide(core.Decision{EventKey: "cal|e", Action: "link", Tasks: []string{"t1"}}, "Linked")
	s.AddRule(core.LearnedRule{Kind: "title", Value: "x", Tasks: []string{"t1", "t2"}})

	if err := s.UpdateCatalog("admin", "Deleted task t1", func(c *core.Catalog) error {
		c.Tasks = c.Tasks[1:]
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	in := s.Inputs()
	if len(in.Manual) != 1 || strings.Join(in.Manual[0].Tasks, ",") != "t2" {
		t.Errorf("manual visits = %+v", in.Manual)
	}
	if len(in.Decisions) != 0 {
		t.Errorf("a link to only the deleted task should go: %+v", in.Decisions)
	}
	if len(in.Rules) != 1 || strings.Join(in.Rules[0].Tasks, ",") != "t2" {
		t.Errorf("rules = %+v", in.Rules)
	}

	if err := s.UpdateCatalog("admin", "Removed calendar", func(c *core.Catalog) error {
		c.Calendars = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(s.Inputs().Events) != 0 || s.CalendarState("cal").SyncToken != "" {
		t.Error("removing a calendar drops its events and sync state")
	}
	a := s.Audit(10)
	if len(a) != 5 || a[0].Action != "Removed calendar" || a[0].By != "admin" {
		t.Errorf("audit = %+v", a)
	}
}

func TestExportAndRestore(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "d.json"))
	secret, _ := s.SessionSecret()
	s.UpdateCatalog("admin", "Set up", func(c *core.Catalog) error {
		c.Sites = []core.Site{{ID: "bkk1", Code: "BKK1", Name: "Bang Na"}}
		return nil
	})
	b, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "session_secret") {
		t.Error("backups must not carry the session signing key")
	}
	s.UpdateCatalog("admin", "Oops", func(c *core.Catalog) error { c.Sites = nil; return nil })
	if err := s.Restore(b, "admin", "Restored"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Catalog().Site("bkk1"); !ok {
		t.Error("restore should bring the site back")
	}
	if got, _ := s.SessionSecret(); string(got) != string(secret) {
		t.Error("restore should keep the current signing key")
	}
	if a := s.Audit(1); len(a) == 0 || a[0].Action != "Restored" {
		t.Error("restore should be in the activity log")
	}
	// The restored state is what's on disk.
	s2, _ := Open(s.path)
	if _, ok := s2.Catalog().Site("bkk1"); !ok {
		t.Error("restore wasn't saved")
	}
	for name, bad := range map[string]string{
		"not json":       "hello",
		"other json":     `{"foo": 1}`,
		"no version":     `{"catalog": {}}`,
		"newer version":  `{"version": 99}`,
		"broken catalog": `{"version": 2, "catalog": {"tasks": [{"id": "t1", "site_id": "nope"}]}}`,
	} {
		if err := s.Restore([]byte(bad), "x", "x"); err == nil {
			t.Errorf("%s: restore should refuse it", name)
		}
	}
	if _, ok := s.Catalog().Site("bkk1"); !ok {
		t.Error("a refused restore must leave the data alone")
	}
}

func TestRestoreIsSafe(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "d.json"))
	key, _ := s.SessionSecret()
	s.SaveUser(auth.User{Username: "mallory", Role: "admin", Epoch: 3}, "t", "")
	b, _ := s.Export()
	// A crafted file with its own signing key.
	var m map[string]any
	json.Unmarshal(b, &m)
	m["session_secret"] = strings.Repeat("A", 44)
	crafted, _ := json.Marshal(m)
	if err := s.Restore(crafted, "admin", "Restored"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.SessionSecret(); string(got) != string(key) {
		t.Error("a restore must never install a signing key from the file")
	}
	if u, _ := s.User("mallory"); u.Epoch <= 3 {
		t.Errorf("restored accounts must be signed out (epoch %d)", u.Epoch)
	}
}

func TestActivityLogKeeps90Days(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	s, _ := Open(filepath.Join(dir, "data.json"))
	s.SetClock(func() time.Time { return now })
	// One entry a day for 120 days.
	start := now.AddDate(0, 0, -119)
	for d := 0; d < 120; d++ {
		now = start.AddDate(0, 0, d)
		s.Log("admin", fmt.Sprintf("Day %d", d))
	}
	all := s.Audit(1000)
	if len(all) != 91 || all[0].Action != "Day 119" || all[90].Action != "Day 29" {
		t.Fatalf("kept %d entries, newest %q, oldest %q", len(all), all[0].Action, all[len(all)-1].Action)
	}
	if got := s.Activity().Oldest(); !got.Equal(start.AddDate(0, 0, 29)) {
		t.Errorf("oldest kept = %v", got)
	}
	// Retention can be made longer (or shorter) by config.
	s.Activity().SetKeepDays(30)
	if n := len(s.Audit(1000)); n != 31 {
		t.Errorf("30-day retention kept %d", n)
	}
	// Files are private and only ever appended to.
	files, _ := os.ReadDir(ActivityDir(filepath.Join(dir, "data.json")))
	info, _ := files[0].Info()
	if info.Mode().Perm() != 0o600 {
		t.Errorf("log file mode %v", info.Mode().Perm())
	}
}

func TestActivityLogSurvivesRestoreAndMovesOldEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	// A data file from before the log had its own files.
	old := `{"version": 2, "audit": [{"at": "2026-10-01T09:00:00Z", "by": "admin", "action": "Added site BKK1"}]}`
	os.WriteFile(path, []byte(old), 0o600)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.SetClock(func() time.Time { return time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC) })
	if a := s.Audit(10); len(a) != 1 || a[0].Action != "Added site BKK1" {
		t.Fatalf("old entries should move to the log: %+v", a)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "Added site BKK1") {
		t.Error("the data file should no longer hold the log")
	}
	backup, _ := s.Export()
	s.LogFrom("somchai", "203.0.113.9", "Signed in")
	// A restore (even of a file carrying its own "audit") can't rewrite the log.
	var m map[string]any
	json.Unmarshal(backup, &m)
	m["audit"] = []map[string]any{{"at": "2026-11-01T00:00:00Z", "by": "x", "action": "forged"}}
	crafted, _ := json.Marshal(m)
	if err := s.Restore(crafted, "admin", "Restored all data from x"); err != nil {
		t.Fatal(err)
	}
	a := s.Audit(10)
	if len(a) != 3 || a[0].Action != "Restored all data from x" || a[1].From != "203.0.113.9" {
		t.Errorf("log after restore = %+v", a)
	}
	for _, e := range a {
		if e.Action == "forged" {
			t.Error("a restore must not add entries from the file")
		}
	}
}

func TestJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.SetClock(func() time.Time { return day("2026-11-04") })
	s.UpdateCatalog("t", "", func(c *core.Catalog) error {
		c.Sites = []core.Site{{ID: "bkk1", Code: "BKK1", Name: "Bang Na"}, {ID: "cnx1", Code: "CNX1", Name: "Chiang Mai"}}
		return nil
	})
	created := day("2026-11-04")
	if _, err := s.AddJob(core.Job{SiteID: "nope", Title: "x", Date: "2026-11-04", By: "nok"}, "add"); err == nil {
		t.Error("a job at a site that doesn't exist should be refused")
	}
	if _, err := s.AddJob(core.Job{SiteID: "bkk1", Title: " ", Date: "2026-11-04", By: "nok"}, "add"); err == nil {
		t.Error("a job with no title should be refused")
	}
	a, err := s.AddJob(core.Job{SiteID: "bkk1", Title: "Repair raised floor", Date: "2026-11-04", By: "nok", Created: created}, "Added a job")
	if err != nil || a.ID == 0 || a.Status != core.JobOpen {
		t.Fatalf("add = %+v, %v", a, err)
	}
	b, _ := s.AddJob(core.Job{SiteID: "cnx1", Title: "Fix door", Date: "2026-11-09", By: "nok", Created: created}, "Added a job")

	// An edit can't move a job to another site or change who added it.
	got, err := s.UpdateJob(a.ID, "som", func(j core.Job) string { return "Edited " + j.Title }, func(j *core.Job) error {
		j.Title, j.SiteID, j.By = "Repair raised floor, row C", "cnx1", "som"
		j.Status, j.DoneOn, j.DoneBy = core.JobDone, "2026-11-04", "som"
		return nil
	})
	if err != nil || got.Title != "Repair raised floor, row C" || got.SiteID != "bkk1" || got.By != "nok" || got.Status != core.JobDone {
		t.Errorf("update = %+v, %v", got, err)
	}
	// A change that doesn't check out leaves the job as it was.
	if _, err := s.UpdateJob(a.ID, "som", func(core.Job) string { return "x" }, func(j *core.Job) error { j.Date = "soon"; return nil }); err == nil {
		t.Error("a bad date should be refused")
	}
	if _, err := s.UpdateJob(999, "som", func(core.Job) string { return "x" }, func(*core.Job) error { return nil }); err != ErrNoJob {
		t.Errorf("missing job = %v", err)
	}

	// Saved to disk.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	jobs := s2.Inputs().Jobs
	if len(jobs) != 2 || jobs[0].Title != "Repair raised floor, row C" || jobs[0].DoneOn != "2026-11-04" {
		t.Fatalf("reopened = %+v", jobs)
	}

	// Backups carry jobs.
	exp, _ := s.Export()
	if !strings.Contains(string(exp), `"Fix door"`) {
		t.Error("backups should hold the jobs")
	}
	if _, err := s.DeleteJob(b.ID, "som", func(j core.Job) string { return "Deleted " + j.Title }); err != nil {
		t.Fatal(err)
	}
	if len(s.Inputs().Jobs) != 1 {
		t.Error("delete didn't remove the job")
	}
	if err := s.Restore(exp, "som", "Restored"); err != nil {
		t.Fatal(err)
	}
	if len(s.Inputs().Jobs) != 2 {
		t.Error("restore should bring the deleted job back")
	}

	// Deleting a site deletes its jobs, as it does its PM tasks.
	s.UpdateCatalog("t", "Deleted CNX1", func(c *core.Catalog) error {
		c.Sites = c.Sites[:1]
		return nil
	})
	if jobs := s.Inputs().Jobs; len(jobs) != 1 || jobs[0].SiteID != "bkk1" {
		t.Errorf("after deleting cnx1: %+v", jobs)
	}

	var log []string
	s.Activity().Each(func(e AuditEntry) error { log = append(log, e.By+": "+e.Action); return nil })
	want := []string{"nok: Added a job", "nok: Added a job", "som: Edited Repair raised floor, row C", "som: Deleted Fix door"}
	for _, w := range want {
		found := false
		for _, l := range log {
			found = found || l == w
		}
		if !found {
			t.Errorf("activity log is missing %q: %v", w, log)
		}
	}
}
