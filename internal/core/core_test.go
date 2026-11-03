package core

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/apinat33198/pmtracker/internal/config"
)

func cfg(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte(`{"timezone": "Asia/Bangkok"}`))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func date(t *testing.T, c *config.Config, s string) time.Time {
	t.Helper()
	d, err := time.ParseInLocation("2006-01-02", s, c.Location())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func testCatalog() Catalog {
	cat := Catalog{
		Sites: []Site{
			{ID: "bkk1", Code: "BKK1", Name: "Bang Na", Region: "Bangkok", Aliases: []string{"บางนา"}},
			{ID: "cnx1", Code: "CNX1", Name: "Chiang Mai", Region: "North"},
			{ID: "old", Code: "OLD1", Name: "Closed site", Region: "North", Archived: true},
		},
		Systems: DefaultSystems(),
		Vendors: []Vendor{
			{Key: "voltedge", Name: "VoltEdge Power", Aliases: []string{"VoltEdge"}, Domains: []string{"voltedge.example"}},
			{Key: "abc", Name: "ABC Power", Domains: []string{"abcpower.example"}},
			{Key: "coolair", Name: "CoolAir Services", Aliases: []string{"CoolAir"}, Domains: []string{"coolair.example"}},
			{Key: "siamups", Name: "Siam UPS Service", Domains: []string{"siamups.example"}},
		},
		Calendars: []Calendar{
			{ID: "pm@group", Label: "PM – Vendors", PMOnly: true},
			{ID: "team@group", Label: "Facilities"},
			{ID: "cnx@group", Label: "Chiang Mai site", SiteID: "cnx1"},
		},
	}
	cat.AddTask(Task{SiteID: "bkk1", System: "dc", Vendor: "voltedge"})                                                 // t1
	cat.AddTask(Task{SiteID: "bkk1", System: "ups", Vendor: "abc"})                                                     // t2
	cat.AddTask(Task{SiteID: "bkk1", System: "crac", Vendor: "coolair"})                                                // t3
	cat.AddTask(Task{SiteID: "cnx1", System: "ups", Vendor: "abc"})                                                     // t4
	cat.AddTask(Task{SiteID: "cnx1", System: "ups", Vendor: "siamups"})                                                 // t5
	cat.AddTask(Task{SiteID: "cnx1", System: "crac", Vendor: "coolair", AnchorMode: "fixed", AnchorDate: "2026-01-15"}) // t6
	cat.AddTask(Task{SiteID: "old", System: "ups", Vendor: "abc"})                                                      // t7 (archived site)
	cat.AddTask(Task{SiteID: "cnx1", System: "dc", Vendor: "voltedge", Paused: true})                                   // t8 (paused)
	return cat
}

func TestCatalogValidate(t *testing.T) {
	cat := testCatalog()
	if err := cat.Validate(); err != nil {
		t.Fatalf("test catalog should be valid: %v", err)
	}
	bad := cat.Clone()
	bad.Sites = append(bad.Sites, Site{ID: "bkk1b", Code: "bkk1", Name: "Dup"})
	bad.Tasks = append(bad.Tasks, Task{ID: "t99", SiteID: "bkk1", System: "ups", Vendor: "abc"})
	bad.Tasks = append(bad.Tasks, Task{ID: "t98", SiteID: "nope", System: "ups", Vendor: "abc"})
	bad.Calendars = append(bad.Calendars, Calendar{ID: "x", Label: "X", SiteID: "gone"})
	err := bad.Validate()
	if err == nil {
		t.Fatal("want errors")
	}
	for _, want := range []string{"Site code bkk1 is used by both", "already has a task for UPS by ABC Power", "points at a site that doesn't exist", "Calendar X points at a site"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in: %v", want, err)
		}
	}
	if Slug("  Hat Yai (South) ") != "hat-yai-south" || Slug("BKK1") != "bkk1" {
		t.Error("slug")
	}
	taken := map[string]bool{"bkk1": true, "bkk1-2": true}
	if got := UniqueSlug("BKK1", func(s string) bool { return taken[s] }); got != "bkk1-3" {
		t.Errorf("unique slug = %s", got)
	}
}

func TestAddMonthsClamp(t *testing.T) {
	cases := []struct {
		in, want string
		n        int
	}{
		{"2026-11-30", "2027-02-28", 3},
		{"2027-11-30", "2028-02-29", 3},
		{"2026-08-31", "2026-11-30", 3},
		{"2026-10-15", "2027-01-15", 3},
		{"2026-05-31", "2025-11-30", -6},
	}
	for _, c := range cases {
		in, _ := time.Parse("2006-01-02", c.in)
		if got := AddMonthsClamp(in, c.n).Format("2006-01-02"); got != c.want {
			t.Errorf("%s %+d months = %s, want %s", c.in, c.n, got, c.want)
		}
	}
}

func TestHasPMWord(t *testing.T) {
	for _, s := range []string{"[PM] UPS – ABC", "Generator PM", "PM visit", "Quarterly PM."} {
		if !hasPMWord(s) {
			t.Errorf("hasPMWord(%q) = false", s)
		}
	}
	for _, s := range []string{"Team sync 3 PM", "Call at 3PM", "Review 15:30 PM", "PMO meeting", "pm visit"} {
		if hasPMWord(s) {
			t.Errorf("hasPMWord(%q) = true", s)
		}
	}
}

func TestMatchMultiSite(t *testing.T) {
	cat := testCatalog()
	m := NewMatcher(&cat, []LearnedRule{{ID: 1, Kind: "series", Value: "s1", Tasks: []string{"t3"}}}, nil)
	ev := func(cal, summary string, mod func(*Event)) Event {
		e := Event{CalendarID: cal, ID: summary, Summary: summary, Status: "confirmed"}
		if mod != nil {
			mod(&e)
		}
		return e
	}
	cases := []struct {
		name       string
		ev         Event
		class      MatchClass
		tasks      string
		candidates string
		missing    string
	}{
		{"convention with vendor", ev("team@group", "[PM] BKK1 UPS – ABC Power", nil), MatchFull, "t2", "", ""},
		{"convention, vendor from the only UPS task", ev("team@group", "[PM] BKK1 UPS", nil), MatchFull, "t2", "", ""},
		{"two systems, two vendors is ambiguous", ev("team@group", "[PM] BKK1 DC, UPS", nil), MatchPartial, "", "t1,t2", "More than one vendor"},
		{"two UPS vendors at one site", ev("team@group", "[PM] CNX1 UPS", nil), MatchPartial, "", "t4,t5", "More than one vendor"},
		{"site by name, keyword PM", ev("team@group", "PM CRAC Chiang Mai", nil), MatchFull, "t6", "", ""},
		{"thai keyword and thai site alias", ev("team@group", "บำรุงรักษา UPS บางนา", nil), MatchFull, "t2", "", ""},
		{"site from location, vendor from title", ev("team@group", "Quarterly PM – ABC Power", func(e *Event) { e.Location = "Chiang Mai data centre" }), MatchFull, "t4", "", ""},
		{"pm calendar + vendor domain covers the vendor's task", ev("pm@group", "BKK1 visit", func(e *Event) { e.Organizer = "tech@voltedge.example" }), MatchFull, "t1", "", ""},
		{"site calendar, no PM keyword", ev("cnx@group", "UPS maintenance", func(e *Event) { e.Organizer = "x@siamups.example" }), MatchPartial, "", "t5", "No PM keyword"},
		{"unknown site", ev("team@group", "[PM] HDY1 UPS", nil), MatchPartial, "", "", "Site not recognised"},
		{"no task for that system", ev("team@group", "[PM] BKK1 Genset", nil), MatchPartial, "", "t1,t2,t3", "has no PM task for Generator"},
		{"two sites in the title", ev("team@group", "[PM] BKK1 & CNX1 UPS", nil), MatchPartial, "", "", "mentions more than one site"},
		{"archived site is ignored", ev("team@group", "[PM] OLD1 UPS – ABC Power", nil), MatchPartial, "", "", "Site not recognised"},
		{"paused task gets no visits", ev("team@group", "[PM] CNX1 DC – VoltEdge", nil), MatchPartial, "", "t4,t5,t6", "no PM task for DC power"},
		{"3 PM is not PM", ev("team@group", "Team sync 3 PM", nil), MatchNone, "", "", ""},
		{"learned series", ev("team@group", "Anything", func(e *Event) { e.RecurringEventID = "s1" }), MatchFull, "t3", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := m.Match(tc.ev, nil)
			if r.Class != tc.class {
				t.Fatalf("class = %v, want %v (reasons %v, missing %v)", r.Class, tc.class, r.Reasons, r.Missing)
			}
			if got := strings.Join(r.Tasks, ","); got != tc.tasks {
				t.Errorf("tasks = %q, want %q", got, tc.tasks)
			}
			if got := strings.Join(r.Candidates, ","); got != tc.candidates {
				t.Errorf("candidates = %q, want %q", got, tc.candidates)
			}
			if tc.missing != "" && !strings.Contains(strings.Join(r.Missing, " | "), tc.missing) {
				t.Errorf("missing = %v, want something with %q", r.Missing, tc.missing)
			}
		})
	}
	if r := m.Match(ev("pm@group", "[PM] BKK1 UPS", nil), &Decision{Action: "ignore"}); r.Class != MatchNone {
		t.Error("ignore wins")
	}
	if r := m.Match(ev("team@group", "x", nil), &Decision{Action: "link", Tasks: []string{"t4", "gone"}}); r.Class != MatchFull || strings.Join(r.Tasks, ",") != "t4" {
		t.Errorf("link keeps live tasks only: %+v", r)
	}
}

func pol(c *config.Config, mode, anchor string) Policy {
	return Task{AnchorMode: mode, AnchorDate: anchor}.Policy(c.Defaults, c.Location())
}

func visit(t *testing.T, c *config.Config, d string, st VisitStatus) Visit {
	return Visit{Date: date(t, c, d), Status: st}
}

func TestForecastRolling(t *testing.T) {
	c := cfg(t)
	fc := NewForecaster(c, date(t, c, "2026-11-03"))
	p := pol(c, "", "")
	cases := []struct {
		name   string
		visits []Visit
		status Status
		due    string
	}{
		{"on track", []Visit{visit(t, c, "2026-10-01", VisitDone)}, StatusOnTrack, "2027-01-01"},
		{"due soon", []Visit{visit(t, c, "2026-08-20", VisitDone)}, StatusDueSoon, "2026-11-20"},
		{"overdue past grace", []Visit{visit(t, c, "2026-07-20", VisitDone)}, StatusOverdue, "2026-10-20"},
		{"inside grace", []Visit{visit(t, c, "2026-07-30", VisitDone)}, StatusDueSoon, "2026-10-30"},
		{"booked", []Visit{visit(t, c, "2026-08-20", VisitDone), visit(t, c, "2026-11-25", VisitScheduled)}, StatusBooked, "2026-11-20"},
		{"off-cycle", []Visit{visit(t, c, "2026-08-20", VisitDone), visit(t, c, "2026-12-20", VisitScheduled)}, StatusOffCycle, "2026-11-20"},
		{"missed does not count", []Visit{visit(t, c, "2026-08-20", VisitDone), visit(t, c, "2026-10-28", VisitMissed)}, StatusDueSoon, "2026-11-20"},
		{"cancelled does not count", []Visit{visit(t, c, "2026-07-20", VisitDone), visit(t, c, "2026-10-20", VisitCancelled)}, StatusOverdue, "2026-10-20"},
		{"no history", nil, StatusNoHistory, ""},
		{"no history but booked", []Visit{visit(t, c, "2026-11-10", VisitScheduled)}, StatusBooked, "2026-11-10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := fc.For(p, tc.visits)
			got := ""
			if f.HasDue {
				got = f.NextDue.Format("2006-01-02")
			}
			if f.Status != tc.status || got != tc.due {
				t.Errorf("got %s %q, want %s %q", f.Status, got, tc.status, tc.due)
			}
		})
	}
	six := Task{IntervalMonths: 6}.Policy(c.Defaults, c.Location())
	if f := fc.For(six, []Visit{visit(t, c, "2026-08-31", VisitDone)}); f.NextDue.Format("2006-01-02") != "2027-02-28" {
		t.Errorf("six-monthly due = %s", f.NextDue.Format("2006-01-02"))
	}
}

func TestForecastFixed(t *testing.T) {
	c := cfg(t)
	fc := NewForecaster(c, date(t, c, "2026-11-03"))
	p := pol(c, AnchorFixed, "2026-01-15")
	f := fc.For(p, []Visit{visit(t, c, "2026-07-16", VisitDone)})
	if f.NextDue.Format("2006-01-02") != "2026-10-15" || f.Status != StatusOverdue {
		t.Errorf("got %s %s", f.NextDue.Format("2006-01-02"), f.Status)
	}
	f = fc.For(p, []Visit{visit(t, c, "2026-10-20", VisitDone)})
	if f.NextDue.Format("2006-01-02") != "2027-01-15" || f.Projections[0].Format("2006-01-02") != "2027-04-15" {
		t.Errorf("got %s, projections %v", f.NextDue.Format("2006-01-02"), f.Projections)
	}
	drift := pol(c, AnchorFixed, "2026-08-31")
	f = NewForecaster(c, date(t, c, "2026-12-15")).For(drift, []Visit{visit(t, c, "2026-11-30", VisitDone)})
	if f.NextDue.Format("2006-01-02") != "2027-02-28" || f.Projections[0].Format("2006-01-02") != "2027-05-31" {
		t.Errorf("drift: due %s, next %s", f.NextDue.Format("2006-01-02"), f.Projections[0].Format("2006-01-02"))
	}
}

func TestNearestWorkingDay(t *testing.T) {
	c := cfg(t)
	if got := NearestWorkingDay(date(t, c, "2026-11-14"), c.IsHoliday).Format("2006-01-02"); got != "2026-11-13" {
		t.Errorf("Saturday → %s", got)
	}
	if got := NearestWorkingDay(date(t, c, "2026-11-15"), c.IsHoliday).Format("2006-01-02"); got != "2026-11-16" {
		t.Errorf("Sunday → %s", got)
	}
}

func TestBuildSitesAndDigest(t *testing.T) {
	c := cfg(t)
	now := date(t, c, "2026-11-03").Add(9 * time.Hour)
	mk := func(id, summary, day string, mod func(*Event)) Event {
		e := Event{CalendarID: "pm@group", ID: id, Summary: summary, Status: "confirmed", Start: date(t, c, day), AllDay: true}
		if mod != nil {
			mod(&e)
		}
		return e
	}
	in := Inputs{
		Catalog: testCatalog(),
		Events: []Event{
			mk("1", "[PM] BKK1 UPS – ABC Power", "2026-07-01", nil),
			mk("2", "[PM] BKK1 UPS – ABC Power", "2026-10-01", func(e *Event) { e.Status = "cancelled" }),
			mk("3", "[PM] BKK1 DC – VoltEdge", "2026-08-25", nil),
			mk("4", "[PM] BKK1 DC – VoltEdge", "2026-11-20", nil),
			mk("5", "[PM] CNX1 UPS", "2026-10-01", func(e *Event) { e.RecurringEventID = "s9" }),
			mk("6", "[PM] CNX1 UPS", "2027-01-01", func(e *Event) { e.RecurringEventID = "s9" }),
		},
		Marks:  map[string]VisitMark{"ev:pm@group|1": {Status: "done"}},
		Manual: []ManualVisit{{ID: 1, Date: "2026-10-10", Tasks: []string{"t3"}}},
	}
	s := Build(c, in, now)

	if len(s.Forecasts) != 6 {
		t.Fatalf("forecasts = %d, want 6 (archived site and paused task left out)", len(s.Forecasts))
	}
	if f, _ := s.Forecast("t2"); f.Status != StatusOverdue {
		t.Errorf("BKK1 UPS = %s, want overdue", f.Status)
	}
	if f, _ := s.Forecast("t1"); f.Status != StatusBooked {
		t.Errorf("BKK1 DC = %s, want booked", f.Status)
	}
	bkk, _ := s.Site("bkk1")
	if bkk.Worst != StatusOverdue || bkk.LastPM.Format("2006-01-02") != "2026-10-10" || bkk.LastPMTask != "t3" {
		t.Errorf("site summary = worst %s, last PM %s (%s)", bkk.Worst, bkk.LastPM.Format("2006-01-02"), bkk.LastPMTask)
	}
	if len(bkk.Cells["ups"]) != 1 || len(bkk.Cells["dc"]) != 1 {
		t.Errorf("cells = %v", bkk.Cells)
	}
	cnx, _ := s.Site("cnx1")
	if len(cnx.Cells["ups"]) != 2 {
		t.Errorf("CNX1 should show two UPS vendors in one cell, got %d", len(cnx.Cells["ups"]))
	}
	if len(s.Sites) != 2 || s.Sites[0].Site.ID != "bkk1" {
		t.Errorf("sites ordered by region then code, archived hidden: %v", len(s.Sites))
	}
	if len(s.Systems) != 3 { // dc, ups, crac in use; genset and fire not
		t.Errorf("matrix columns = %d", len(s.Systems))
	}
	if len(s.Inbox) != 1 || s.Inbox[0].Siblings != 1 || strings.Join(s.Inbox[0].Match.Candidates, ",") != "t4,t5" {
		t.Errorf("inbox = %+v", s.Inbox)
	}
	if len(s.Upcoming) == 0 || s.Upcoming[0].Task.ID != "t2" {
		t.Errorf("upcoming should start with the overdue task")
	}
	var abc VendorSummary
	for _, v := range s.Vendors {
		if v.Vendor.Key == "abc" {
			abc = v
		}
	}
	if abc.Sites != 2 || abc.Tasks != 2 || abc.Counts[StatusOverdue] != 1 || s.Vendors[0].Vendor.Key != "abc" {
		t.Errorf("vendor summary = %+v (first %s)", abc, s.Vendors[0].Vendor.Key)
	}
	d := Digest(s, "")
	for _, want := range []string{"Overdue (1)", "BKK1 UPS (ABC Power): due 1 Oct, 33 days late", "Review inbox: 1 event"} {
		if !strings.Contains(d, want) {
			t.Errorf("digest missing %q:\n%s", want, d)
		}
	}
}

func TestImport(t *testing.T) {
	c := cfg(t)
	cat := testCatalog()
	now := date(t, c, "2026-11-03")
	csv := "\ufeffsite_code,site_name,region,system,vendor,interval_months,last_pm_date\n" +
		"HDY1,Hat Yai,South,UPS,Andaman Power,3,15/09/2569\n" + // new site + vendor, Buddhist year
		"BKK1,,,UPS,ABC Power,6,2026-08-01\n" + // existing task: interval change; last PM older than on record
		"BKK1,,,Battery room,ABC Power,,\n" + // new system
		"CNX1,,,UPS,,3,\n" + // missing vendor
		"CNX1,,,CRAC,CoolAir,3,2027-01-01\n" // future date
	last := map[string]time.Time{"t2": date(t, c, "2026-09-01")}
	res, visits, err := ApplyImport(&cat, strings.NewReader(csv), func(id string) time.Time { return last[id] }, now, c.Location(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if res.SitesCreated != 1 || res.VendorsCreated != 1 || res.SystemsCreated != 1 || res.TasksCreated != 2 || res.TasksUpdated != 1 || res.Errors != 2 {
		t.Errorf("result = %+v", res)
	}
	if len(visits) != 1 || visits[0].Date != "2026-09-15" {
		t.Errorf("visits = %+v (only HDY1's date is newer than what is on record)", visits)
	}
	hdy, ok := cat.SiteByCode("HDY1")
	if !ok || hdy.Name != "Hat Yai" || hdy.Region != "South" || len(cat.TasksAt(hdy.ID)) != 1 {
		t.Errorf("HDY1 = %+v", hdy)
	}
	if tk, _ := cat.Task("t2"); tk.IntervalMonths != 6 {
		t.Errorf("t2 interval = %d", tk.IntervalMonths)
	}
	if _, _, err := ApplyImport(&cat, strings.NewReader("code,name\nx,y\n"), nil, now, c.Location(), ""); err == nil {
		t.Error("want a header error")
	}
}

func TestICS(t *testing.T) {
	c := cfg(t)
	now := date(t, c, "2026-11-03")
	in := Inputs{Catalog: testCatalog(), Events: []Event{{CalendarID: "pm@group", ID: "1", Status: "confirmed",
		Summary: "[PM] BKK1 UPS – ABC Power", Start: date(t, c, "2026-09-01"), AllDay: true}}}
	out := ForecastICS(c, Build(c, in, now), now)
	for _, want := range []string{"BEGIN:VCALENDAR\r\n", "DTSTART;VALUE=DATE:20261201\r\n", "SUMMARY:PM due: BKK1 UPS (ABC Power)\r\n", "END:VCALENDAR\r\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("ics missing %q", want)
		}
	}
	for _, line := range strings.Split(out, "\r\n") {
		if len(line) > 75 {
			t.Errorf("unfolded line: %q", line)
		}
	}
	for _, part := range strings.Split(foldICS("DESCRIPTION:"+strings.Repeat("บำรุงรักษา ", 20)), "\r\n") {
		if len(part) > 75 || !utf8.ValidString(part) {
			t.Errorf("bad fold part %q", part)
		}
	}
}
