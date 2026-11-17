package core

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRiskBands(t *testing.T) {
	for _, c := range []struct {
		l, c  int
		score int
		band  string
		tone  Status
	}{
		{1, 1, 1, "low", StatusOnTrack}, {2, 2, 4, "low", StatusOnTrack}, {1, 5, 5, "medium", StatusDueSoon},
		{3, 3, 9, "medium", StatusDueSoon}, {2, 5, 10, "high", StatusOverdue}, {4, 4, 16, "high", StatusOverdue},
		{4, 5, 20, "extreme", StatusOverdue}, {5, 5, 25, "extreme", StatusOverdue},
	} {
		r := RiskOf(c.l, c.c)
		if r.Score != c.score || r.Key() != c.band || r.Tone() != c.tone {
			t.Errorf("RiskOf(%d, %d) = %d %s %s", c.l, c.c, r.Score, r.Key(), r.Tone())
		}
	}
	if r := RiskOf(0, 3); r.Rated() || r.Text() != "Not rated" {
		t.Errorf("an unrated risk = %+v", r)
	}
	if r := RiskOf(6, 1); r.Rated() {
		t.Error("ratings run 1 to 5")
	}
	if got := RiskOf(4, 4).Text(); got != "High (4 × 4 = 16)" {
		t.Errorf("Text = %q", got)
	}
	for _, b := range RiskBands {
		if b.Tone() != RiskOf(1, max(1, min(b.Min, 5))).Tone() {
			t.Errorf("band %s tone", b.Key)
		}
	}
}

func goodIncident() Incident {
	return Incident{SiteID: "bkk1", Kind: IncidentKind, Category: "power", Title: "UPS on bypass", Occurred: "2026-11-01T14:30",
		DetectedBy: "alarm", Impact: 2, Consequence: 5, Likelihood: 3, Status: IncidentOpen}
}

func TestIncidentClean(t *testing.T) {
	in := goodIncident()
	in.Title, in.Detail = "  UPS   on\tbypass ", "line one\r\nline two‮"
	in.Status = ""
	if err := in.Clean(); err != nil {
		t.Fatal(err)
	}
	if in.Title != "UPS on bypass" || in.Detail != "line one\nline two" || in.Status != IncidentOpen {
		t.Errorf("cleaned = %q %q %q", in.Title, in.Detail, in.Status)
	}
	closed := func(mod func(*Incident)) Incident {
		x := goodIncident()
		x.Status, x.ClosedOn, x.Restored = IncidentClosed, "2026-11-03", "2026-11-01T15:10"
		x.Cause, x.Action, x.Treatment, x.ResidualLikelihood, x.ResidualConsequence = "Module fault", "Module replaced", "reduce", 2, 3
		if mod != nil {
			mod(&x)
		}
		return x
	}
	if x := closed(nil); x.Clean() != nil {
		t.Errorf("a complete closed incident: %v", x.Clean())
	}
	for want, mod := range map[string]func(*Incident){
		"pick the site":                   func(x *Incident) { x.SiteID = "" },
		"in a line":                       func(x *Incident) { x.Title = "​ " },
		"incident or a near miss":         func(x *Incident) { x.Kind = "accident" },
		"what kind of incident":           func(x *Incident) { x.Category = "" },
		"how it was noticed":              func(x *Incident) { x.DetectedBy = "" },
		"a near miss had no impact":       func(x *Incident) { x.Kind = NearMiss },
		"an incident had some impact":     func(x *Incident) { x.Impact = 0 },
		"can't be less than":              func(x *Incident) { x.Impact, x.Consequence = 4, 3 },
		"likely it is to happen again":    func(x *Incident) { x.Likelihood = 0 },
		"worst consequence":               func(x *Incident) { x.Consequence = 6 },
		"rate both":                       func(x *Incident) { x.ResidualLikelihood = 2 },
		"as a date and time":              func(x *Incident) { x.Occurred = "2026-11-01" },
		"noticed before it happened":      func(x *Incident) { x.Detected = "2026-11-01T14:00" },
		"restored before it happened":     func(x *Incident) { x.Restored = "2026-10-31T23:00" },
		"target date can't be before":     func(x *Incident) { x.Due = "2026-10-31" },
		"enter when service was restored": func(x *Incident) { x.Status = IncidentRestored },
		"unknown incident status":         func(x *Incident) { x.Status = "fixed" },
		"unknown treatment":               func(x *Incident) { x.Treatment = "ignore" },
		"a timeline entry":                func(x *Incident) { x.Notes = []IncidentNote{{At: time.Now()}} },
	} {
		x := goodIncident()
		mod(&x)
		if err := x.Clean(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
	}
	for want, mod := range map[string]func(*Incident){
		"root cause before closing":   func(x *Incident) { x.Cause = "" },
		"action taken before closing": func(x *Incident) { x.Action = " " },
		"how the risk is treated":     func(x *Incident) { x.Treatment = "" },
		"re-rate the risk":            func(x *Incident) { x.ResidualLikelihood, x.ResidualConsequence = 0, 0 },
		"restored before closing":     func(x *Incident) { x.Restored = "" },
		"closed before it happened":   func(x *Incident) { x.ClosedOn = "2026-10-30" },
	} {
		x := closed(mod)
		if err := x.Clean(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("closing, %s: got %v", want, err)
		}
	}
	// A near miss closes without a restore time; reopening clears the close.
	nm := closed(func(x *Incident) { x.Kind, x.Impact, x.Restored = NearMiss, 0, "" })
	if err := nm.Clean(); err != nil {
		t.Errorf("a closed near miss: %v", err)
	}
	nm.Status = IncidentContain
	if nm.Clean(); nm.ClosedOn != "" || nm.ClosedBy != "" {
		t.Error("reopening should clear the close")
	}
}

func TestIncidentLinks(t *testing.T) {
	cat := Catalog{Systems: DefaultSystems()}
	cat.Sites = []Site{{ID: "bkk1", Code: "BKK1"}, {ID: "cnx1", Code: "CNX1"}}
	cat.AddTask(Task{SiteID: "bkk1", System: "ups", Vendor: "abc"})
	cat.AddTask(Task{SiteID: "cnx1", System: "crac", Vendor: "cool"})
	bkkUPS, cnxCRAC := cat.TasksAt("bkk1")[0].ID, cat.TasksAt("cnx1")[0].ID
	findings := []Finding{{ID: 3, SiteID: "bkk1"}, {ID: 4, SiteID: "cnx1"}}
	first := goodIncident()
	first.ID, first.Ref = 10, "INC-2026-001"
	all := []Incident{first}

	in := goodIncident()
	in.ID, in.Occurred, in.TaskID, in.FindingID, in.RepeatOf = 11, "2026-11-05T08:00", bkkUPS, 3, 10
	if err := in.CheckLinks(&cat, findings, all); err != nil {
		t.Fatal(err)
	}
	for want, mod := range map[string]func(*Incident){
		"site no longer exists":     func(x *Incident) { x.SiteID = "gone" },
		"this site's systems":       func(x *Incident) { x.TaskID = cnxCRAC },
		"finding #4 is at another":  func(x *Incident) { x.FindingID = 4 },
		"no finding #9":             func(x *Incident) { x.FindingID = 9 },
		"happened after this one":   func(x *Incident) { x.Occurred = "2026-10-01T08:00" },
		"no incident #99":           func(x *Incident) { x.RepeatOf = 99 },
		"is at another site; a rep": func(x *Incident) { x.SiteID, x.TaskID, x.FindingID = "cnx1", "", 0 },
	} {
		x := in
		mod(&x)
		if err := x.CheckLinks(&cat, findings, all); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
	}
	// The earlier one can't be moved after its repeat.
	all = append(all, in)
	moved := first
	moved.Occurred = "2026-11-06T00:00"
	if err := moved.CheckLinks(&cat, findings, all); err == nil || !strings.Contains(err.Error(), "repeat of this one") {
		t.Errorf("moving past a repeat: %v", err)
	}
}

func TestNextIncidentRef(t *testing.T) {
	all := []Incident{{Ref: "INC-2026-001"}, {Ref: "INC-2026-009"}, {Ref: "INC-2025-030"}, {Ref: "odd"}}
	if got := NextIncidentRef(all, 2026); got != "INC-2026-010" {
		t.Errorf("2026: %s", got)
	}
	if got := NextIncidentRef(all, 2027); got != "INC-2027-001" {
		t.Errorf("2027: %s", got)
	}
	if got := NextIncidentRef([]Incident{{Ref: "INC-2026-999"}}, 2026); got != "INC-2026-1000" {
		t.Errorf("past 999: %s", got)
	}
}

func TestIncidentChanges(t *testing.T) {
	cat := Catalog{}
	a := goodIncident()
	b := a
	if got := IncidentChanges(a, b, &cat); len(got) != 0 {
		t.Errorf("no change = %v", got)
	}
	b.Status, b.Likelihood, b.Cause, b.Owner = IncidentContain, 4, "Fan failed", "ABC Power"
	got := strings.Join(IncidentChanges(a, b, &cat), "; ")
	for _, w := range []string{"Status: Open → Contained", "Likelihood: 3 Possible → 4 Likely", "Risk: High (3 × 5 = 15) → Extreme (4 × 5 = 20)",
		"Root cause added", "Owner: none → ABC Power"} {
		if !strings.Contains(got, w) {
			t.Errorf("changes %q lack %q", got, w)
		}
	}
	b = a
	b.Kind, b.Impact = NearMiss, 0
	if got := strings.Join(IncidentChanges(a, b, &cat), "; "); !strings.Contains(got, "Impact: 2 Minor → 0 None") {
		t.Errorf("impact change = %q", got)
	}
}

func TestIncidentViewsAndStats(t *testing.T) {
	loc := time.UTC
	today := time.Date(2026, 11, 17, 0, 0, 0, 0, loc)
	cat := Catalog{Sites: []Site{{ID: "bkk1", Code: "BKK1"}, {ID: "old", Code: "OLD", Archived: true}}}
	mk := func(id int, mod func(*Incident)) Incident {
		x := goodIncident()
		x.ID, x.Ref = id, "INC-2026-00"+string(rune('0'+id))
		mod(&x)
		return x
	}
	list := []Incident{
		mk(1, func(x *Incident) { // closed, restored in 40 minutes, re-rated low
			x.Occurred, x.Restored, x.Status, x.ClosedOn = "2026-09-01T10:00", "2026-09-01T10:40", IncidentClosed, "2026-09-11"
			x.Cause, x.Action, x.Treatment, x.ResidualLikelihood, x.ResidualConsequence = "c", "a", "reduce", 1, 3
		}),
		mk(2, func(x *Incident) { // ongoing, extreme, past its target
			x.Occurred, x.Likelihood, x.Status, x.Due, x.RepeatOf = "2026-11-16T08:00", 4, IncidentContain, "2026-11-16", 1
		}),
		mk(3, func(x *Incident) { // restored after 2 hours, follow-up pending
			x.Occurred, x.Restored, x.Status, x.DetectedBy = "2026-11-10T01:00", "2026-11-10T03:00", IncidentRestored, "staff"
		}),
		mk(4, func(x *Incident) {
			x.Kind, x.Impact, x.Likelihood, x.Consequence, x.Occurred = NearMiss, 0, 1, 2, "2026-11-12T09:00"
		}),
		mk(5, func(x *Incident) { x.Occurred = "2024-01-01T00:00"; x.Status = IncidentClosed }), // long ago (status isn't checked here)
		mk(6, func(x *Incident) { x.SiteID = "old" }),                                           // archived site: left out
	}
	list[4].ClosedOn = "2024-01-02"
	views := buildIncidents(&cat, list, today, loc)
	var order []int
	for _, v := range views {
		order = append(order, v.ID)
	}
	if want := []int{2, 4, 3, 1, 5}; fmt.Sprint(order) != fmt.Sprint(want) {
		t.Errorf("order = %v, want %v", order, want)
	}
	v2 := views[0]
	if !v2.Ongoing() || !v2.Late() || v2.Tone() != StatusOverdue || v2.Risk.Key() != "extreme" || !strings.Contains(v2.Hint(), "Extreme risk") {
		t.Errorf("incident 2: %+v %s", v2.Risk, v2.Hint())
	}
	v1 := views[3]
	if v1.RestoreMinutes() != 40 || v1.Current().Key() != "low" || v1.Age != 10 || v1.Tone() != StatusOnTrack {
		t.Errorf("incident 1: restore %d, current %s, age %d", v1.RestoreMinutes(), v1.Current().Key(), v1.Age)
	}
	if views[1].RestoreMinutes() != -1 || !views[1].Ongoing() {
		t.Error("a near miss has no restore time")
	}

	st := IncidentStatsOf(views, today.AddDate(0, -12, 0))
	if st.Open != 3 || st.Ongoing != 2 || st.OpenHigh != 2 || st.Late != 1 || st.Incidents != 3 || st.NearMisses != 1 || st.Repeats != 1 ||
		st.Closed != 1 || st.MedianClose != 10 || st.Restored != 2 || st.MedianRestore != 80 || st.AlarmShare != 75 {
		t.Errorf("stats = %+v", st)
	}
	if st.NearMissRatio() != "0.3" || st.RepeatShare() != 25 {
		t.Errorf("ratio %s, repeats %d", st.NearMissRatio(), st.RepeatShare())
	}
	if (IncidentStats{NearMisses: 2}).NearMissRatio() != "2 : 0" || (IncidentStats{}).NearMissRatio() != "–" {
		t.Error("near-miss ratio with no incidents")
	}

	m := RiskMatrix(views, today.AddDate(0, -12, 0))
	count := func(l, c int) int { return m[5-l].Cells[c-1].Count }
	// 2 at 4×5, 3 at 3×5, 4 at 1×2, 1 at its residual 1×3; 5 is old and closed.
	if count(4, 5) != 1 || count(3, 5) != 1 || count(1, 2) != 1 || count(1, 3) != 1 || count(3, 3) != 0 {
		t.Errorf("matrix: %v", m)
	}
	if m[0].Likelihood.N != 5 || m[0].Cells[4].Risk.Key() != "extreme" {
		t.Error("the matrix runs likelihood 5 at the top, consequence 5 on the right")
	}
	if g := IncidentsByCategory(views, today.AddDate(0, -12, 0)); len(g) != 1 || g[0].Name != "Power" || g[0].Stats.Open != 3 {
		t.Errorf("by category = %+v", g)
	}
}

func TestDigestListsIncidents(t *testing.T) {
	loc := time.UTC
	today := time.Date(2026, 11, 17, 0, 0, 0, 0, loc)
	cat := Catalog{Sites: []Site{{ID: "bkk1", Code: "BKK1"}}}
	ongoing := goodIncident()
	ongoing.ID, ongoing.Ref, ongoing.Status, ongoing.Owner, ongoing.Due = 1, "INC-2026-001", IncidentContain, "ABC Power", "2026-11-18"
	low := goodIncident()
	low.ID, low.Ref, low.Kind, low.Impact, low.Consequence, low.Likelihood, low.Status, low.Restored = 2, "INC-2026-002", IncidentKind, 1, 1, 2, IncidentRestored, "2026-11-01T15:00"
	s := Snapshot{Today: today, Catalog: &cat, Incidents: buildIncidents(&cat, []Incident{ongoing, low}, today, loc)}
	d := Digest(s, "")
	if !strings.Contains(d, "Incidents needing attention (1)\n• BKK1 INC-2026-001 UPS on bypass (contained, high risk): action in 1 day (ABC Power)") {
		t.Errorf("digest:\n%s", d)
	}
}
