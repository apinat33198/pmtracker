package core

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/apinat33198/pmtracker/internal/config"
)

// EventView is a mirrored event with its match result.
type EventView struct {
	Event    Event
	Match    Match
	Decision *Decision
	VisitID  string
	Siblings int // other unresolved occurrences of the same series (inbox only)
}

// SiteSummary rolls a site's tasks up for the overview.
type SiteSummary struct {
	Site       Site
	Forecasts  []Forecast // urgency order
	Worst      Status
	LastPM     time.Time // most recent completed PM across the site's tasks
	LastPMTask string
	NextDue    time.Time
	Counts     map[Status]int
	Cells      map[string][]Forecast // system key → tasks for that system
}

// VendorSummary rolls tasks up by vendor.
type VendorSummary struct {
	Vendor Vendor
	Sites  int
	Tasks  int
	Counts map[Status]int
}

// Snapshot is the whole derived state, rebuilt on every request.
type Snapshot struct {
	Today       time.Time
	Catalog     *Catalog
	Events      []EventView // newest first
	Visits      []Visit     // newest first
	Forecasts   []Forecast  // urgency order
	Sites       []SiteSummary
	Systems     []System // systems with at least one active task (matrix columns)
	Vendors     []VendorSummary
	Upcoming    []Forecast // overdue, or due/booked within 30 days, by date
	Inbox       []EventView
	Counts      map[Status]int
	Unconfirmed int
}

func (s Snapshot) Forecast(task string) (Forecast, bool) {
	for _, f := range s.Forecasts {
		if f.Task.ID == task {
			return f, true
		}
	}
	return Forecast{}, false
}

func (s Snapshot) Site(id string) (SiteSummary, bool) {
	for _, ss := range s.Sites {
		if ss.Site.ID == id {
			return ss, true
		}
	}
	return SiteSummary{}, false
}

// Build derives visits, forecasts, summaries and the review inbox.
func Build(cfg *config.Config, in Inputs, now time.Time) Snapshot {
	loc := cfg.Location()
	today := Day(now, loc)
	cat := in.Catalog
	m := NewMatcher(&cat, in.Rules, cfg.Keywords)

	changes := map[string][]Change{}
	for _, c := range in.Changes {
		changes[c.EventKey] = append(changes[c.EventKey], c)
	}
	snap := Snapshot{Today: today, Catalog: &cat, Counts: map[Status]int{}}
	var visits []Visit
	inboxSeen := map[string]int{}

	events := append([]Event(nil), in.Events...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].Start.After(events[j].Start) })
	for _, ev := range events {
		var dec *Decision
		if d, ok := in.Decisions[ev.Key()]; ok {
			dec = &d
		}
		mt := m.Match(ev, dec)
		view := EventView{Event: ev, Match: mt, Decision: dec}
		if mt.Class == MatchFull {
			evCopy := ev
			v := Visit{ID: "ev:" + ev.Key(), Date: Day(ev.Start, loc), Tasks: mt.Tasks, Source: "calendar",
				Method: mt.Method, Event: &evCopy, Reschedules: changes[ev.Key()]}
			v.Status = visitStatus(ev.Cancelled(), v.Date, today, in.Marks[v.ID])
			visits = append(visits, v)
			view.VisitID = v.ID
		}
		if mt.Class == MatchPartial && !ev.Cancelled() {
			key := ev.Key()
			if ev.RecurringEventID != "" {
				key = "series:" + ev.CalendarID + "|" + ev.RecurringEventID
			}
			if idx, ok := inboxSeen[key]; ok {
				cur := snap.Inbox[idx]
				if abs(DaysBetween(today, ev.Start)) < abs(DaysBetween(today, cur.Event.Start)) {
					view.Siblings = cur.Siblings + 1
					snap.Inbox[idx] = view
				} else {
					snap.Inbox[idx].Siblings++
				}
			} else {
				inboxSeen[key] = len(snap.Inbox)
				snap.Inbox = append(snap.Inbox, view)
			}
		}
		snap.Events = append(snap.Events, view)
	}

	for _, mv := range in.Manual {
		d, err := time.ParseInLocation("2006-01-02", mv.Date, loc)
		if err != nil {
			continue
		}
		v := Visit{ID: fmt.Sprintf("man:%d", mv.ID), Date: d, Tasks: mv.Tasks, Source: "manual", Method: "manual",
			Note: mv.Note, ManualID: mv.ID, Status: VisitDone}
		if mk, ok := in.Marks[v.ID]; ok && mk.Status == "missed" {
			v.Status = VisitMissed
		}
		visits = append(visits, v)
	}
	sort.SliceStable(visits, func(i, j int) bool { return visits[i].Date.After(visits[j].Date) })
	snap.Visits = visits

	byTask := map[string][]Visit{}
	for _, v := range visits {
		for _, t := range v.Tasks {
			byTask[t] = append(byTask[t], v)
		}
		if v.Status == VisitUnconfirmed {
			snap.Unconfirmed++
		}
	}

	fc := NewForecaster(cfg, now)
	usedSystems := map[string]bool{}
	bySite := map[string][]Forecast{}
	for _, t := range cat.Tasks {
		site, ok := cat.Site(t.SiteID)
		if !ok || site.Archived || t.Paused {
			continue
		}
		f := fc.For(t.Policy(cfg.Defaults, loc), byTask[t.ID])
		f.Task, f.Site = t, site
		snap.Forecasts = append(snap.Forecasts, f)
		snap.Counts[f.Status]++
		usedSystems[t.System] = true
		bySite[t.SiteID] = append(bySite[t.SiteID], f)
	}
	SortForecasts(snap.Forecasts)

	for _, s := range cat.Systems {
		if usedSystems[s.Key] {
			snap.Systems = append(snap.Systems, s)
		}
	}

	sites := append([]Site(nil), cat.Sites...)
	SortSites(sites)
	for _, s := range sites {
		if s.Archived {
			continue
		}
		ss := SiteSummary{Site: s, Forecasts: bySite[s.ID], Counts: map[Status]int{}, Cells: map[string][]Forecast{}, Worst: StatusOnTrack}
		SortForecasts(ss.Forecasts)
		for i, f := range ss.Forecasts {
			if i == 0 {
				ss.Worst = f.Status
			}
			ss.Counts[f.Status]++
			ss.Cells[f.Task.System] = append(ss.Cells[f.Task.System], f)
			if f.LastDone != nil && f.LastDone.Date.After(ss.LastPM) {
				ss.LastPM, ss.LastPMTask = f.LastDone.Date, f.Task.ID
			}
			if f.HasDue && (ss.NextDue.IsZero() || f.NextDue.Before(ss.NextDue)) {
				ss.NextDue = f.NextDue
			}
		}
		if len(ss.Forecasts) == 0 {
			ss.Worst = StatusNoHistory
		}
		snap.Sites = append(snap.Sites, ss)
	}

	vend := map[string]*VendorSummary{}
	vendSites := map[string]map[string]bool{}
	for _, f := range snap.Forecasts {
		vs, ok := vend[f.Task.Vendor]
		if !ok {
			v, _ := cat.Vendor(f.Task.Vendor)
			vs = &VendorSummary{Vendor: v, Counts: map[Status]int{}}
			vend[f.Task.Vendor] = vs
			vendSites[f.Task.Vendor] = map[string]bool{}
		}
		vs.Tasks++
		vs.Counts[f.Status]++
		vendSites[f.Task.Vendor][f.Task.SiteID] = true
	}
	for k, vs := range vend {
		vs.Sites = len(vendSites[k])
		snap.Vendors = append(snap.Vendors, *vs)
	}
	sort.Slice(snap.Vendors, func(i, j int) bool {
		a, b := snap.Vendors[i], snap.Vendors[j]
		ua := a.Counts[StatusOverdue]*100 + a.Counts[StatusDueSoon]*10 + a.Counts[StatusOffCycle]
		ub := b.Counts[StatusOverdue]*100 + b.Counts[StatusDueSoon]*10 + b.Counts[StatusOffCycle]
		if ua != ub {
			return ua > ub
		}
		return a.Vendor.Name < b.Vendor.Name
	})

	horizon := today.AddDate(0, 0, 30)
	for _, f := range snap.Forecasts {
		if f.HasDue && (f.Status == StatusOverdue || !f.ActionDate().After(horizon) || !f.NextDue.After(horizon)) {
			snap.Upcoming = append(snap.Upcoming, f)
		}
	}
	sort.SliceStable(snap.Upcoming, func(i, j int) bool { return snap.Upcoming[i].ActionDate().Before(snap.Upcoming[j].ActionDate()) })

	sort.SliceStable(snap.Inbox, func(i, j int) bool {
		return abs(DaysBetween(today, snap.Inbox[i].Event.Start)) < abs(DaysBetween(today, snap.Inbox[j].Event.Start))
	})
	return snap
}

func visitStatus(cancelled bool, date, today time.Time, mark VisitMark) VisitStatus {
	switch {
	case cancelled:
		return VisitCancelled
	case mark.Status == "missed":
		return VisitMissed
	case mark.Status == "done":
		return VisitDone
	case !date.Before(today):
		return VisitScheduled
	default:
		return VisitUnconfirmed
	}
}

// Digest is the daily reminder text. Empty means nothing needs attention.
func Digest(s Snapshot, baseURL string) string {
	var b strings.Builder
	section := func(title string, st Status) {
		var lines []string
		for _, f := range s.Forecasts {
			if f.Status != st {
				continue
			}
			line := fmt.Sprintf("• %s: due %s", s.Catalog.TaskLabel(f.Task.ID), f.NextDue.Format("2 Jan"))
			switch {
			case f.DaysLeft < 0:
				line += fmt.Sprintf(", %d days late", -f.DaysLeft)
			case f.DaysLeft == 0:
				line += ", today"
			default:
				line += fmt.Sprintf(", in %d days", f.DaysLeft)
			}
			if st == StatusOffCycle && f.Booked != nil {
				line += fmt.Sprintf("; booked %s", f.Booked.Date.Format("2 Jan"))
			}
			lines = append(lines, line)
		}
		if len(lines) > 0 {
			fmt.Fprintf(&b, "%s (%d)\n%s\n\n", title, len(lines), strings.Join(lines, "\n"))
		}
	}
	section("Overdue", StatusOverdue)
	section("Due soon, not booked", StatusDueSoon)
	section("Booked off-cycle", StatusOffCycle)
	if n := len(s.Inbox); n == 1 {
		b.WriteString("Review inbox: 1 event needs a decision\n")
	} else if n > 1 {
		fmt.Fprintf(&b, "Review inbox: %d events need a decision\n", n)
	}
	if n := s.Unconfirmed; n == 1 {
		b.WriteString("Awaiting confirmation: 1 past visit\n")
	} else if n > 1 {
		fmt.Fprintf(&b, "Awaiting confirmation: %d past visits\n", n)
	}
	if b.Len() == 0 {
		return ""
	}
	out := fmt.Sprintf("PM digest for %s, %d sites\n\n%s", s.Today.Format("Mon 2 Jan 2006"), len(s.Sites), b.String())
	if baseURL != "" {
		out += "\n" + strings.TrimRight(baseURL, "/") + "/"
	}
	return strings.TrimSpace(out)
}
