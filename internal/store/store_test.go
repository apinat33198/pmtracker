package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if err := s2.ResetCalendar("cal"); err != nil || s2.CalendarState("cal").SyncToken != "" || len(s2.Inputs().Events) != 0 {
		t.Error("reset should clear token and events")
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
