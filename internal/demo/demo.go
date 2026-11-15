// Package demo provides 21 sites with DC, UPS and CRAC vendors and a fake
// calendar of PM visits laid out relative to today, so every status shows up
// whenever it runs.
package demo

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/apinat33198/pmtracker/internal/auth"
	"github.com/apinat33198/pmtracker/internal/config"
	"github.com/apinat33198/pmtracker/internal/core"
)

const (
	PMCal   = "pm-vendors@demo"
	FacCal  = "facilities@demo"
	SiteCal = "hkt1-site@demo"
)

// Config is the demo configuration.
func Config() (*config.Config, error) {
	return config.Parse([]byte(`{
	  "timezone": "Asia/Bangkok",
	  "data_file": "pmtracker-demo.json",
	  "feed": {"token": "demo"},
	  "auth": {"anonymous_access": "viewer"}
	}`))
}

// Users are the demo's accounts; every password is "demo".
func Users(now time.Time) ([]auth.User, error) {
	hash, err := auth.Hash("demo")
	if err != nil {
		return nil, err
	}
	var out []auth.User
	for _, role := range []string{config.RoleAdmin, config.RoleEditor, config.RoleViewer} {
		out = append(out, auth.User{Username: role, Role: role, PasswordHash: hash, Created: now})
	}
	return out, nil
}

type siteDef struct {
	code, name, region string
	aliases            []string
	genset             bool
}

var sites = []siteDef{
	{"BKK1", "Bang Na", "Bangkok Metro", []string{"บางนา"}, true},
	{"BKK2", "Lat Krabang", "Bangkok Metro", nil, true},
	{"NBI1", "Nonthaburi", "Bangkok Metro", nil, false},
	{"SPK1", "Samut Prakan", "Bangkok Metro", nil, false},
	{"AYA1", "Ayutthaya", "Central", nil, false},
	{"SRI1", "Saraburi", "Central", nil, false},
	{"NSN1", "Nakhon Sawan", "Central", nil, false},
	{"CBI1", "Chonburi", "East", nil, true},
	{"RYG1", "Rayong", "East", nil, false},
	{"CTI1", "Chanthaburi", "East", nil, false},
	{"NMA1", "Nakhon Ratchasima", "Northeast", []string{"Korat", "โคราช"}, true},
	{"KKC1", "Khon Kaen", "Northeast", []string{"ขอนแก่น"}, false},
	{"UTH1", "Udon Thani", "Northeast", nil, false},
	{"UBP1", "Ubon Ratchathani", "Northeast", []string{"Ubon"}, false},
	{"CNX1", "Chiang Mai", "North", []string{"เชียงใหม่"}, false},
	{"LPG1", "Lampang", "North", nil, false},
	{"PLK1", "Phitsanulok", "North", nil, false},
	{"SNI1", "Surat Thani", "South", nil, false},
	{"HKT1", "Phuket", "South", nil, true},
	{"HDY1", "Hat Yai", "South", []string{"หาดใหญ่"}, false},
	{"TRG1", "Trang", "South", nil, false},
}

// Catalog returns the demo sites, systems, vendors, tasks and calendars.
func Catalog(now time.Time) core.Catalog {
	cat := core.Catalog{
		Systems: core.DefaultSystems(),
		Vendors: []core.Vendor{
			{Key: "voltedge", Name: "VoltEdge Power", Aliases: []string{"VoltEdge"}, Domains: []string{"voltedge.example"}, Contact: "Khun Arthit", Phone: "02 000 1100", Email: "service@voltedge.example"},
			{Key: "abcpower", Name: "ABC Power", Domains: []string{"abcpower.example"}, Contact: "Khun Nok", Phone: "08 0000 2200", Email: "service@abcpower.example"},
			{Key: "siamups", Name: "Siam UPS Service", Aliases: []string{"Siam UPS"}, Domains: []string{"siamups.example"}, Phone: "053 000 330", Email: "booking@siamups.example"},
			{Key: "coolair", Name: "CoolAir Services", Aliases: []string{"CoolAir"}, Domains: []string{"coolair.example"}, Contact: "Khun Somchai", Phone: "02 000 4400"},
			{Key: "northcool", Name: "Northern Cooling", Domains: []string{"northcool.example"}, Phone: "043 000 550", Email: "pm@northcool.example"},
			{Key: "andaman", Name: "Andaman HVAC", Aliases: []string{"Andaman"}, Domains: []string{"andaman-hvac.example"}, Phone: "076 000 660"},
			{Key: "powergen", Name: "PowerGen Siam", Aliases: []string{"PowerGen"}, Domains: []string{"powergen.example"}, Contact: "Service desk", Phone: "02 000 7700", Email: "desk@powergen.example"},
		},
		Calendars: []core.Calendar{
			{ID: PMCal, Label: "PM – Vendors", PMOnly: true},
			{ID: FacCal, Label: "Facilities team"},
			{ID: SiteCal, Label: "Phuket site", SiteID: "hkt1"},
		},
	}
	created := now.AddDate(-2, 0, 0)
	for _, s := range sites {
		id := core.Slug(s.code)
		cat.Sites = append(cat.Sites, core.Site{ID: id, Code: s.code, Name: s.name, Region: s.region, Aliases: s.aliases, Created: created})
		ups, crac := "abcpower", "coolair"
		switch s.region {
		case "North", "Northeast":
			ups, crac = "siamups", "northcool"
		case "South":
			ups, crac = "siamups", "andaman"
		}
		cat.AddTask(core.Task{SiteID: id, System: "dc", Vendor: "voltedge", Created: created})
		cat.AddTask(core.Task{SiteID: id, System: "ups", Vendor: ups, Created: created})
		if s.code == "BKK2" { // a second UPS room serviced by another vendor
			cat.AddTask(core.Task{SiteID: id, System: "ups", Vendor: "siamups", Notes: "New UPS room, level 3", Created: created})
		}
		t := core.Task{SiteID: id, System: "crac", Vendor: crac, Created: created}
		if s.code == "HKT1" {
			t.AnchorMode = core.AnchorFixed // contract quarters
		}
		cat.AddTask(t)
		if s.genset {
			cat.AddTask(core.Task{SiteID: id, System: "genset", Vendor: "powergen", IntervalMonths: 6, Created: created})
		}
	}
	return cat
}

// Source is the fake calendar.
type Source struct {
	loc   *time.Location
	today time.Time
	mu    sync.Mutex
	base  []core.Event
	moved string // event moved by the vendor on the first incremental sync
	gone  string // event cancelled on the first incremental sync
}

type scenario int

const (
	onTrack scenario = iota
	booked
	dueSoon
	overdue
	offCycle
	noHistory
)

// NewSource lays out events for cat. The fixed-contract task's anchor is
// written back into cat so its slots line up with the generated visits.
func NewSource(cfg *config.Config, cat *core.Catalog, now time.Time) *Source {
	loc := cfg.Location()
	s := &Source{loc: loc, today: core.Day(now, loc)}
	rng := rand.New(rand.NewPCG(2026, 11))

	// Exact counts, shuffled: a believable spread across 70 tasks.
	var plan []scenario
	add := func(sc scenario, n int) {
		for i := 0; i < n; i++ {
			plan = append(plan, sc)
		}
	}
	add(overdue, 5)
	add(dueSoon, 7)
	add(offCycle, 3)
	add(booked, 20)
	for len(plan) < len(cat.Tasks) {
		plan = append(plan, onTrack)
	}
	rng.Shuffle(len(plan), func(i, j int) { plan[i], plan[j] = plan[j], plan[i] })
	// Two tasks have no history: one gets its visits from a vendor series
	// that has to be linked in the inbox, one is a newly added site.
	special := map[string]scenario{"uth1|dc": noHistory, "trg1|crac": noHistory, "hkt1|crac": booked}

	day := func(off int) time.Time { return s.today.AddDate(0, 0, off) }
	var firstBooked, firstOverdue string
	for i := range cat.Tasks {
		t := &cat.Tasks[i]
		site, _ := cat.Site(t.SiteID)
		sc := plan[i]
		if v, ok := special[t.SiteID+"|"+t.System]; ok {
			sc = v
		}
		if sc == noHistory {
			continue
		}
		interval := t.IntervalMonths
		if interval == 0 {
			interval = 3
		}
		cycle := int(float64(interval) * 30.44)
		var ago, book int // days since last PM; booking offset from due (0 = none)
		hasBook := false
		switch sc {
		case onTrack:
			ago = 5 + rng.IntN(max(1, cycle-45))
		case booked:
			ago = cycle - 26 + rng.IntN(30)
			book, hasBook = rng.IntN(15)-7, true
		case dueSoon:
			ago = cycle - 26 + rng.IntN(28)
		case overdue:
			ago = cycle + 12 + rng.IntN(45)
		case offCycle:
			ago = cycle - 24 + rng.IntN(18)
			book, hasBook = 30+rng.IntN(16), true
		}
		last := day(-ago)
		if t.AnchorMode == core.AnchorFixed {
			t.AnchorDate = core.AddMonthsClamp(last, -interval*8).Format("2006-01-02")
		}
		for n := 0; ; n++ {
			d := core.AddMonthsClamp(last, -interval*n)
			if n > 0 {
				d = d.AddDate(0, 0, rng.IntN(11)-5)
			}
			if core.DaysBetween(d, s.today) > 700 {
				break
			}
			s.base = append(s.base, s.visit(t, site, fmt.Sprintf("%s-%d", t.ID, n), d, rng))
		}
		due := core.AddMonthsClamp(last, interval)
		if hasBook {
			b := due.AddDate(0, 0, book)
			if !b.After(s.today) {
				b = day(1 + rng.IntN(5))
			}
			ev := s.visit(t, site, t.ID+"-next", b, rng)
			s.base = append(s.base, ev)
			if sc == booked && firstBooked == "" {
				firstBooked = ev.Key()
			}
		}
		if sc == overdue && firstOverdue == "" {
			// The vendor booked the visit, then cancelled it and never rebooked.
			ev := s.visit(t, site, t.ID+"-cancelled", due.AddDate(0, 0, 2), rng)
			s.base = append(s.base, ev)
			firstOverdue = ev.Key()
		}
	}
	s.moved, s.gone = firstBooked, firstOverdue
	s.base = append(s.base, s.extras(day)...)
	sort.Slice(s.base, func(i, j int) bool { return s.base[i].Start.Before(s.base[j].Start) })
	return s
}

// visit builds one PM event titled the way that vendor usually writes it.
func (s *Source) visit(t *core.Task, site core.Site, id string, d time.Time, rng *rand.Rand) core.Event {
	hour := []int{9, 10, 13}[rng.IntN(3)]
	cal, title, org := PMCal, "", ""
	thai := site.Code
	for _, a := range site.Aliases {
		if a != "" && a[0] >= 0x80 {
			thai = a
		}
	}
	switch t.Vendor {
	case "voltedge":
		title, org = fmt.Sprintf("[PM] %s DC – VoltEdge", site.Code), "planner@voltedge.example"
	case "abcpower":
		title, org = fmt.Sprintf("PM UPS %s – ABC Power", site.Code), "service@abcpower.example"
	case "siamups":
		title, org = fmt.Sprintf("บำรุงรักษา UPS %s", thai), "booking@siamups.example"
		if site.Code == "BKK2" {
			title = "บำรุงรักษา UPS BKK2 ชั้น 3" // second UPS room
		}
	case "coolair":
		title, org = fmt.Sprintf("[PM] %s CRAC – CoolAir", site.Code), "service@coolair.example"
	case "northcool":
		cal, title, org = FacCal, fmt.Sprintf("Preventive maintenance CRAC %s", site.Name), "pm@northcool.example"
	case "andaman":
		title, org = fmt.Sprintf("[PM] %s PAC", site.Code), "ops@andaman-hvac.example"
	case "powergen":
		title, org = fmt.Sprintf("[PM] %s Genset – PowerGen", site.Code), "desk@powergen.example"
	}
	if site.Code == "HKT1" { // the Phuket site keeps its own calendar, no site code in titles
		cal = SiteCal
		title = map[string]string{"dc": "DC PM – VoltEdge", "ups": "UPS PM – Siam UPS", "crac": "PAC PM", "genset": "Genset PM – PowerGen"}[t.System]
	}
	start := time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, s.loc)
	return core.Event{CalendarID: cal, ID: id, ICalUID: id + "@demo", Status: "confirmed", Summary: title,
		Start: start, End: start.Add(3 * time.Hour), Organizer: org, Location: site.Name, Updated: d.AddDate(0, 0, -10)}
}

// extras are events the matcher can't place on its own, plus everyday noise.
func (s *Source) extras(day func(int) time.Time) []core.Event {
	ev := func(cal, id, title string, off, hour int, mod func(*core.Event)) core.Event {
		d := day(off)
		e := core.Event{CalendarID: cal, ID: id, ICalUID: id + "@demo", Status: "confirmed", Summary: title,
			Organizer: "facilities@demo", Updated: d.AddDate(0, 0, -7)}
		if hour < 0 {
			e.Start, e.End, e.AllDay = d, d.AddDate(0, 0, 1), true
		} else {
			e.Start = time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, s.loc)
			e.End = e.Start.Add(2 * time.Hour)
		}
		if mod != nil {
			mod(&e)
		}
		return e
	}
	var out []core.Event
	// VoltEdge's recurring invite for Udon Thani: no PM keyword, site only in
	// the location. Linking the series once gives UTH1 DC its whole history.
	for _, off := range []int{-276, -185, -94, -3, 88} {
		d := day(off)
		out = append(out, ev(FacCal, "voltedge-uth_"+d.Format("20060102"), "Quarterly maintenance visit", off, 9, func(e *core.Event) {
			e.RecurringEventID, e.Organizer, e.Location = "voltedge-uth", "planner@voltedge.example", "Udon Thani exchange building"
		}))
	}
	out = append(out,
		ev(PMCal, "bkk2-ups-ambiguous", "[PM] BKK2 UPS", 16, 10, nil), // two UPS vendors at BKK2
		ev(PMCal, "unknown-site", "[PM] PTT1 UPS – ABC Power", 21, 10, nil),
		ev(FacCal, "thai-no-site", "บำรุงรักษาระบบไฟฟ้า", 9, -1, nil),
		ev(FacCal, "abc-survey", "Site survey BKK2 – ABC Power", -9, 14, func(e *core.Event) { e.Attendees = []string{"sales@abcpower.example"} }),
		ev(FacCal, "ops-1", "Weekly ops sync 3 PM", -2, 15, nil),
		ev(FacCal, "ops-2", "Weekly ops sync 3 PM", 5, 15, nil),
		ev(FacCal, "budget", "Budget review", 6, 10, nil),
		ev(FacCal, "drill", "Fire drill briefing, Bang Na", 12, 9, nil),
	)
	return out
}

// List implements gcal.Source: a full listing, then on the first
// incremental sync a vendor reschedule and a cancellation.
func (s *Source) List(_ context.Context, calendarID, syncToken string, _ time.Time) ([]core.Event, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch syncToken {
	case "":
		var out []core.Event
		for _, e := range s.base {
			if e.CalendarID == calendarID {
				out = append(out, e)
			}
		}
		return out, "demo-1", nil
	case "demo-1":
		var out []core.Event
		for _, e := range s.base {
			if e.CalendarID != calendarID {
				continue
			}
			switch e.Key() {
			case s.moved:
				e.Start, e.End = e.Start.AddDate(0, 0, 3), e.End.AddDate(0, 0, 3) // the vendor pushed it back
				out = append(out, e)
			case s.gone:
				out = append(out, core.Event{CalendarID: e.CalendarID, ID: e.ID, Status: "cancelled"})
			}
		}
		return out, "demo-2", nil
	}
	return nil, syncToken, nil
}

// Jobs are a few ad-hoc jobs around today: one late, one today, one coming
// up and one done.
func Jobs(now time.Time, loc *time.Location) []core.Job {
	today := core.Day(now, loc)
	d := func(n int) string { return today.AddDate(0, 0, n).Format("2006-01-02") }
	return []core.Job{
		{SiteID: "bkk2", Title: "Replace cracked raised-floor tiles by rack row C", Date: d(-3), Who: "Facility team", Note: "Two tiles lifting; keep the area taped off.", Status: core.JobOpen, By: "demo", Created: now.AddDate(0, 0, -6)},
		{SiteID: "bkk1", Title: "Repair raised floor", Date: d(0), Who: "Siam Floor Works", Note: "Quote QT-2026-118", Status: core.JobOpen, By: "demo", Created: now.AddDate(0, 0, -1)},
		{SiteID: "cnx1", Title: "Fix door access reader", Date: d(5), Who: "Khun Somchai", Status: core.JobOpen, By: "demo", Created: now.AddDate(0, 0, -2)},
		{SiteID: "hkt1", Title: "Clear condensate drain, CRAC 2", Date: d(-9), Who: "Andaman HVAC", Status: core.JobDone, DoneOn: d(-8), DoneBy: "demo", By: "demo", Created: now.AddDate(0, 0, -12)},
	}
}

// SeedFindings adds findings around today through add: open, in progress
// and closed ones at several sites, one past its target, and a CRAC alarm
// at CNX1 that keeps coming back because the fix never reached the cause.
func SeedFindings(cat core.Catalog, now time.Time, loc *time.Location, add func(core.Finding) (core.Finding, error)) error {
	today := core.Day(now, loc)
	d := func(n int) string { return today.AddDate(0, 0, n).Format("2006-01-02") }
	task := func(site, system string) string {
		for _, t := range cat.TasksAt(site) {
			if t.System == system {
				return t.ID
			}
		}
		return ""
	}
	type seed struct {
		core.Finding
		repeat int // index of the finding in this list it repeats, plus one
	}
	f := func(site, system, found, kind, severity, title string) core.Finding {
		return core.Finding{SiteID: site, TaskID: task(site, system), FoundOn: found, Kind: kind, Severity: severity, Title: title,
			Status: core.FindingOpen, By: "demo", Created: today.AddDate(0, 0, -1)}
	}
	with := func(x core.Finding, mod func(*core.Finding)) core.Finding { mod(&x); return x }
	closed := func(x core.Finding, on, action string) core.Finding {
		x.Status, x.ClosedOn, x.ClosedBy, x.Action = core.FindingClosed, on, "demo", action
		return x
	}
	seeds := []seed{
		{Finding: with(f("bkk1", "ups", d(-24), "equipment", "high", "Battery string 2: three cells over the internal-resistance limit"), func(x *core.Finding) {
			x.Reading, x.Owner, x.Due, x.Spread = "6.1 to 6.4 mΩ, limit 5.5", "ABC Power", d(-3), "todo"
			x.Detail = "Cells 3, 7 and 11. ABC Power to quote a replacement string. Same battery batch went to BKK2 and CBI1."
		})},
		{Finding: with(f("bkk1", "crac", d(-24), "equipment", "medium", "CRAC 2 supply air 2 °C above setpoint"), func(x *core.Finding) {
			x.Reading, x.Owner, x.Due, x.Status = "Supply 20 °C, setpoint 18 °C", "CoolAir Services", d(10), core.FindingDoing
			x.Cause = "Filters clogged early: the room has been dustier since the renovation next door, and filters are changed every 6 months."
		})},
		{Finding: closed(with(f("cnx1", "crac", d(-190), "equipment", "high", "CRAC 1 high-pressure alarm"), func(x *core.Finding) {
			x.Owner, x.Due = "Northern Cooling", d(-186)
		}), d(-187), "Condenser coil cleaned.")},
		{Finding: closed(with(f("cnx1", "crac", d(-100), "equipment", "high", "CRAC 1 high-pressure alarm again"), func(x *core.Finding) {
			x.Owner, x.Due = "Northern Cooling", d(-97)
			x.Cause = "Condenser coil clogged with dust from the building site next to the outdoor unit."
		}), d(-94), "Coil cleaned again."), repeat: 3},
		{Finding: with(f("cnx1", "crac", d(-8), "equipment", "high", "CRAC 1 high-pressure alarm, third time"), func(x *core.Finding) {
			x.Owner, x.Due = "Northern Cooling", d(6)
			x.Detail = "Same alarm as the last two PM rounds. Cleaning the coil hasn't stopped it."
			x.Cause = "Dust from the building site still reaches the condenser. Cleaning treats the symptom."
		}), repeat: 4},
		{Finding: closed(with(f("bkk2", "", d(-40), "5s", "low", "Cardboard boxes stored behind rack row B"), func(x *core.Finding) {
			x.Owner, x.Due, x.Spread = "Facility team", d(-33), "todo"
			x.Cause = "No place to put packaging after installs, so it stayed in the room."
		}), d(-38), "Removed. Packaging now goes to the store room the same day; added to the PM walk-round checklist.")},
		{Finding: closed(with(f("hkt1", "", d(-60), "safety", "medium", "Fire extinguisher inspection tags expired"), func(x *core.Finding) {
			x.Owner, x.Due, x.Spread = "Facility team", d(-46), "done"
		}), d(-50), "Extinguishers re-inspected. Tag dates added to the yearly checklist; checked at all sites.")},
		{Finding: closed(with(f("nma1", "dc", d(-70), "equipment", "medium", "Rectifier module 3 fan noisy"), func(x *core.Finding) {
			x.Owner, x.Due = "VoltEdge Power", d(-40)
		}), d(-30), "Fan replaced by VoltEdge on the next visit.")},
		{Finding: with(f("kkc1", "", d(-15), "idea", "low", "Ask vendors for readings as numbers, not ticks"), func(x *core.Finding) {
			x.Owner, x.Spread = "PM coordinator", "todo"
			x.Detail = "UPS and DC reports only tick OK. Ask for battery voltage and internal resistance per string, so trends show before a cell fails."
		})},
		{Finding: f("hdy1", "ups", d(-5), "5s", "low", "UPS bypass breaker has no label")},
	}
	ids := make([]int, len(seeds))
	for i, s := range seeds {
		if s.repeat > 0 {
			s.RepeatOf = ids[s.repeat-1]
		}
		saved, err := add(s.Finding)
		if err != nil {
			return fmt.Errorf("demo finding %q: %w", s.Title, err)
		}
		ids[i] = saved.ID
	}
	return nil
}
