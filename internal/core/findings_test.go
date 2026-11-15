package core

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func finding(id int, site, task, found string) Finding {
	return Finding{ID: id, SiteID: site, TaskID: task, FoundOn: found, Kind: "equipment", Severity: "medium", Title: "x", Status: FindingOpen}
}

func TestFindingClean(t *testing.T) {
	f := Finding{SiteID: "bkk1", FoundOn: "2026-11-04", Kind: "equipment", Severity: "high", Title: " Battery\n string 2 ",
		Reading: " 6.2  mΩ ", Detail: " cell 3\r\ncell 7\x07 ", Owner: "\u202eABC Power", ClosedOn: "2026-11-05", ClosedBy: "nok"}
	if err := f.Clean(); err != nil {
		t.Fatal(err)
	}
	if f.Title != "Battery string 2" || f.Reading != "6.2 mΩ" || f.Detail != "cell 3\ncell 7" || f.Owner != "ABC Power" ||
		f.Status != FindingOpen || f.ClosedOn != "" || f.ClosedBy != "" {
		t.Errorf("cleaned = %+v", f)
	}
	ok := func() Finding {
		return Finding{SiteID: "bkk1", FoundOn: "2026-11-04", Kind: "5s", Severity: "low", Title: "Boxes behind rack B"}
	}
	bad := map[string]func(*Finding){
		"no site":             func(f *Finding) { f.SiteID = "" },
		"no title":            func(f *Finding) { f.Title = "\u200b " },
		"long title":          func(f *Finding) { f.Title = strings.Repeat("ก", MaxFindingTitle+1) },
		"long action":         func(f *Finding) { f.Action = strings.Repeat("x", MaxFindingText+1) },
		"bad date":            func(f *Finding) { f.FoundOn = "4/11/2026" },
		"mistyped year":       func(f *Finding) { f.FoundOn = "0026-11-04" },
		"unknown kind":        func(f *Finding) { f.Kind = "other" },
		"no severity":         func(f *Finding) { f.Severity = "" },
		"unknown spread":      func(f *Finding) { f.Spread = "maybe" },
		"target before found": func(f *Finding) { f.Due = "2026-11-03" },
		"bad target":          func(f *Finding) { f.Due = "soon" },
		"closed no date":      func(f *Finding) { f.Status, f.Action = FindingClosed, "Removed" },
		"closed no action":    func(f *Finding) { f.Status, f.ClosedOn = FindingClosed, "2026-11-05" },
		"closed before found": func(f *Finding) { f.Status, f.ClosedOn, f.Action = FindingClosed, "2026-11-01", "Removed" },
		"unknown status":      func(f *Finding) { f.Status = "parked" },
		"repeat of itself":    func(f *Finding) { f.ID, f.RepeatOf = 4, 4 },
	}
	for name, mod := range bad {
		f := ok()
		mod(&f)
		if err := f.Clean(); err == nil {
			t.Errorf("%s: %+v should be refused", name, f)
		}
	}
	good := ok()
	good.Status, good.ClosedOn, good.Action, good.Due, good.Spread = FindingClosed, "2026-11-04", "Moved to the store room", "2026-11-04", "todo"
	if err := good.Clean(); err != nil {
		t.Errorf("closed the same day: %v", err)
	}
}

func TestFindingLinks(t *testing.T) {
	cat := testCatalog()
	all := []Finding{finding(1, "bkk1", "t3", "2026-08-01"), finding(2, "cnx1", "", "2026-08-01"), finding(3, "bkk1", "", "2026-11-04")}
	cases := []struct {
		f    Finding
		want string
	}{
		{finding(9, "gone", "", "2026-11-04"), "no longer exists"},
		{finding(9, "bkk1", "t4", "2026-11-04"), "this site's PM tasks"}, // t4 is at cnx1
		{finding(9, "bkk1", "t99", "2026-11-04"), "this site's PM tasks"},
		{func() Finding { f := finding(9, "bkk1", "", "2026-11-04"); f.RepeatOf = 2; return f }(), "another site"},
		{func() Finding { f := finding(9, "bkk1", "", "2026-07-01"); f.RepeatOf = 1; return f }(), "found after this one"},
		{func() Finding { f := finding(9, "bkk1", "", "2026-11-04"); f.RepeatOf = 42; return f }(), "no finding #42"},
		// Moving #1 after #3 would be fine, but not after a repeat of it.
		{func() Finding { f := all[0]; f.FoundOn = "2026-12-01"; return f }(), ""},
	}
	for i, c := range cases {
		err := c.f.CheckLinks(&cat, all)
		if (c.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.want)) {
			t.Errorf("case %d: %v, want %q", i, err, c.want)
		}
	}
	rep := finding(9, "bkk1", "t3", "2026-11-04")
	rep.RepeatOf = 1
	if err := rep.CheckLinks(&cat, all); err != nil {
		t.Errorf("a repeat of an earlier finding at the site: %v", err)
	}
	// Found the same day: the lower number counts as earlier.
	same := finding(9, "bkk1", "", "2026-11-04")
	same.RepeatOf = 3
	if err := same.CheckLinks(&cat, all); err != nil {
		t.Errorf("same day, later number: %v", err)
	}
	all = append(all, rep)
	moved := all[0]
	moved.FoundOn = "2026-12-01"
	if err := moved.CheckLinks(&cat, all); err == nil || !strings.Contains(err.Error(), "#9 is recorded as a repeat") {
		t.Errorf("moving a finding after its repeat = %v", err)
	}
}

func TestBuildFindingsAndStats(t *testing.T) {
	c := cfg(t)
	now := date(t, c, "2026-11-04").Add(9 * time.Hour)
	closed := func(f Finding, on, action string) Finding {
		f.Status, f.ClosedOn, f.Action = FindingClosed, on, action
		return f
	}
	with := func(f Finding, mod func(*Finding)) Finding { mod(&f); return f }
	in := Inputs{Catalog: testCatalog(), Findings: []Finding{
		closed(with(finding(1, "bkk1", "t3", "2026-08-01"), func(f *Finding) { f.Due = "2026-08-15" }), "2026-08-11", "Cleaned the coil"),
		with(finding(2, "bkk1", "t3", "2026-11-01"), func(f *Finding) { f.RepeatOf, f.Severity, f.Title = 1, "high", "High-pressure alarm again" }),
		with(finding(3, "bkk1", "t2", "2026-10-01"), func(f *Finding) { f.Due, f.Owner, f.Title = "2026-10-31", "ABC Power", "Battery string 2" }),
		with(finding(4, "cnx1", "", "2026-10-20"), func(f *Finding) { f.Status, f.Severity, f.Kind = FindingDoing, "low", "5s" }),
		closed(with(finding(5, "cnx1", "t6", "2026-09-01"), func(f *Finding) { f.Due, f.Spread = "2026-09-05", "todo" }), "2026-09-20", "Replaced filter"),
		finding(6, "old", "t7", "2026-10-01"),
		finding(7, "gone", "", "2026-10-01"),
		closed(finding(8, "bkk1", "t1", "2025-06-01"), "2025-06-30", "Old one"),
	}}
	snap := Build(c, in, now)
	var ids []int
	for _, f := range snap.Findings {
		ids = append(ids, f.ID)
	}
	if !reflect.DeepEqual(ids, []int{2, 3, 4, 5, 1, 8}) {
		t.Errorf("order = %v, want open (high, then late, then oldest), then most recently closed; none at archived or deleted sites", ids)
	}
	if snap.OpenFindings != 3 {
		t.Errorf("open = %d", snap.OpenFindings)
	}
	f3, _ := snap.Finding(3)
	if !f3.Late() || f3.DaysLeft != -4 || f3.Tone() != StatusOverdue || f3.StatusLabel() != "Past target" || f3.Age != 34 || !f3.HasTask || f3.Task.System != "ups" {
		t.Errorf("late finding = %+v", f3)
	}
	f1, _ := snap.Finding(1)
	if f1.Age != 10 || !f1.ClosedOnTime() || !reflect.DeepEqual(f1.Repeats, []int{2}) || f1.Tone() != StatusOnTrack {
		t.Errorf("closed finding = %+v", f1)
	}
	if f4, _ := snap.Finding(4); f4.StatusLabel() != "In progress" || f4.Tone() != StatusOffCycle || f4.KindLabel() != "Housekeeping (5S)" || f4.HasTask {
		t.Errorf("in progress = %+v", f4)
	}
	if got := len(snap.FindingsAt("bkk1")); got != 4 {
		t.Errorf("bkk1 has %d findings", got)
	}
	if got := snap.FindingsFor("t3"); len(got) != 2 || got[0].ID != 2 {
		t.Errorf("t3 findings = %+v", got)
	}

	since := snap.Today.AddDate(-1, 0, 0)
	st := StatsOf(snap.Findings, since)
	want := FindingStats{Open: 3, OpenHigh: 1, Doing: 1, Late: 1, SpreadTodo: 1, Found: 5, Repeats: 1, Closed: 2, MedianClose: 15, WithTarget: 2, OnTime: 1}
	if st != want {
		t.Errorf("stats = %+v\nwant    %+v", st, want)
	}
	if st.RepeatShare() != 20 || st.OnTimeShare() != 50 || (FindingStats{}).RepeatShare() != -1 {
		t.Errorf("shares = %d%%, %d%%", st.RepeatShare(), st.OnTimeShare())
	}
	sites := FindingsBySite(snap.Findings, since)
	if len(sites) != 2 || sites[0].Code != "BKK1" || sites[0].Stats.OpenHigh != 1 || sites[1].Stats.Open != 1 {
		t.Errorf("by site = %+v", sites)
	}
	vendors := FindingsByVendor(snap.Findings, snap.Catalog, since)
	var names []string
	for _, v := range vendors {
		names = append(names, v.Name)
	}
	if !reflect.DeepEqual(names, []string{"CoolAir Services", "ABC Power"}) {
		t.Errorf("by vendor = %v (VoltEdge's only finding is older than a year)", names)
	}

	// Findings never touch PM.
	plain := in
	plain.Findings = nil
	if p := Build(c, plain, now); !reflect.DeepEqual(p.Forecasts, snap.Forecasts) {
		t.Error("findings must not change PM forecasts")
	}
	d := Digest(snap, "")
	if !strings.Contains(d, "Findings past their target date (1)\n• BKK1 #3 Battery string 2 (medium): 4 days late (ABC Power)") || strings.Contains(d, "alarm again") {
		t.Errorf("digest should list findings past their target:\n%s", d)
	}
}
