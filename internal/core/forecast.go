package core

import (
	"sort"
	"time"

	"github.com/apinat33198/pmtracker/internal/config"
)

// Forecast is one task's PM position as of Today.
type Forecast struct {
	Task     Task
	Site     Site
	Policy   Policy
	Status   Status
	Today    time.Time
	LastDone *Visit
	Booked   *Visit // next scheduled visit, if any
	NextDue  time.Time
	HasDue   bool
	DaysLeft int // today → NextDue; negative when past due
	// BookedOffset is days from NextDue to the booked visit.
	BookedOffset int
	// SuggestedBooking is NextDue moved off a weekend or holiday (zero if not needed).
	SuggestedBooking time.Time
	Projections      []time.Time
	Visits           []Visit // newest first
	Unconfirmed      int
	FixedNoHistory   bool
}

func (f Forecast) WindowStart() time.Time { return f.NextDue.AddDate(0, 0, -f.Policy.WindowDays) }
func (f Forecast) WindowEnd() time.Time   { return f.NextDue.AddDate(0, 0, f.Policy.WindowDays) }

// ActionDate is when something next happens: the booking if any, else the due date.
func (f Forecast) ActionDate() time.Time {
	if f.Booked != nil {
		return f.Booked.Date
	}
	return f.NextDue
}

// Forecaster computes forecasts for a fixed "today".
type Forecaster struct {
	cfg   *config.Config
	today time.Time
}

func NewForecaster(cfg *config.Config, today time.Time) *Forecaster {
	return &Forecaster{cfg: cfg, today: Day(today, cfg.Location())}
}

// For forecasts one task from the visits that include it.
func (fc *Forecaster) For(p Policy, visits []Visit) Forecast {
	today := fc.today
	f := Forecast{Policy: p, Today: today}

	asc := append([]Visit(nil), visits...)
	sort.SliceStable(asc, func(i, j int) bool { return asc[i].Date.Before(asc[j].Date) })
	for i := range asc {
		v := &asc[i]
		if v.Status.Counts() && !v.Date.After(today) {
			f.LastDone = v
		}
		if v.Status == VisitUnconfirmed {
			f.Unconfirmed++
		}
	}
	for i := range asc {
		v := &asc[i]
		if v.Status == VisitScheduled && !v.Date.Before(today) {
			f.Booked = v
			break
		}
	}

	interval := p.IntervalMonths
	// Fixed mode: always compute slot n from the anchor, never chain from the
	// previous slot, so month-end dates don't drift (31 Aug → 30 Nov → 28 Feb …).
	slot := func(n int) time.Time { return AddMonthsClamp(p.Anchor, n*interval) }
	dueSlot := -1
	if p.AnchorMode == AnchorFixed && !p.Anchor.IsZero() {
		after := today.AddDate(0, 0, -p.WindowDays-1) // the due slot's window must start after this date
		if f.LastDone != nil {
			after = f.LastDone.Date
		} else {
			f.FixedNoHistory = true
		}
		for n := 0; n < 1200; n++ {
			if s := slot(n); s.AddDate(0, 0, -p.WindowDays).After(after) {
				f.NextDue, f.HasDue, dueSlot = s, true, n
				break
			}
		}
	} else if f.LastDone != nil {
		f.NextDue, f.HasDue = AddMonthsClamp(f.LastDone.Date, interval), true
	} else if !p.Anchor.IsZero() {
		f.NextDue, f.HasDue = p.Anchor, true
	}

	if !f.HasDue {
		if f.Booked != nil {
			f.NextDue, f.HasDue = f.Booked.Date, true
		} else {
			f.Status = StatusNoHistory
		}
	}
	if f.HasDue {
		f.DaysLeft = DaysBetween(today, f.NextDue)
		switch {
		case f.Booked != nil:
			f.BookedOffset = DaysBetween(f.NextDue, f.Booked.Date)
			if abs(f.BookedOffset) <= p.WindowDays {
				f.Status = StatusBooked
			} else {
				f.Status = StatusOffCycle
			}
		case today.After(f.NextDue.AddDate(0, 0, p.GraceDays)):
			f.Status = StatusOverdue
		case f.DaysLeft <= p.LeadDays:
			f.Status = StatusDueSoon
		default:
			f.Status = StatusOnTrack
		}
		if s := NearestWorkingDay(f.NextDue, fc.cfg.IsHoliday); !s.Equal(f.NextDue) {
			f.SuggestedBooking = s
		}
		if dueSlot >= 0 {
			for n := 1; n <= 3; n++ {
				f.Projections = append(f.Projections, slot(dueSlot+n))
			}
		} else {
			base := f.NextDue
			if f.Booked != nil {
				base = f.Booked.Date
			}
			for n := 1; n <= 3; n++ {
				f.Projections = append(f.Projections, AddMonthsClamp(base, n*interval))
			}
		}
	}
	for i := len(asc) - 1; i >= 0; i-- {
		f.Visits = append(f.Visits, asc[i])
	}
	return f
}

// SortForecasts orders by urgency, then by days left.
func SortForecasts(fs []Forecast) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Status.Rank() != fs[j].Status.Rank() {
			return fs[i].Status.Rank() < fs[j].Status.Rank()
		}
		if fs[i].HasDue != fs[j].HasDue {
			return fs[i].HasDue
		}
		return fs[i].DaysLeft < fs[j].DaysLeft
	})
}
