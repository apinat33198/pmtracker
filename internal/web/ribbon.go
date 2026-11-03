package web

import (
	"fmt"
	"html"
	"html/template"
	"strings"
	"time"

	"github.com/apinat33198/pmtracker/internal/core"
)

// ribbonSpec sets the scale of a service ribbon: days before and after today.
type ribbonSpec struct {
	Width, Height int
	Before, After int
	Labels        bool    // month labels along the top
	LabelY        float64 // baseline of the month labels
}

var (
	rowRibbon   = ribbonSpec{Width: 320, Height: 30, Before: 183, After: 275}
	axisRibbon  = ribbonSpec{Width: 320, Height: 26, Before: 183, After: 275, Labels: true, LabelY: 23}
	assetRibbon = ribbonSpec{Width: 1100, Height: 66, Before: 365, After: 365, Labels: true, LabelY: 11}
)

type ribbon struct {
	spec  ribbonSpec
	today time.Time
	start time.Time
	b     strings.Builder
}

func newRibbon(spec ribbonSpec, today time.Time) *ribbon {
	return &ribbon{spec: spec, today: today, start: today.AddDate(0, 0, -spec.Before)}
}

func (r *ribbon) x(t time.Time) float64 {
	span := float64(r.spec.Before + r.spec.After)
	d := float64(core.DaysBetween(r.start, t))
	return 6 + d/span*float64(r.spec.Width-12)
}

func (r *ribbon) in(t time.Time) bool {
	d := core.DaysBetween(r.start, t)
	return d >= 0 && d <= r.spec.Before+r.spec.After
}

func (r *ribbon) f(format string, a ...any) { fmt.Fprintf(&r.b, format, a...) }

func esc(s string) string { return html.EscapeString(s) }

func (r *ribbon) months(y0, y1 float64) {
	end := r.today.AddDate(0, 0, r.spec.After)
	m := time.Date(r.start.Year(), r.start.Month(), 1, 0, 0, 0, 0, r.today.Location()).AddDate(0, 1, 0)
	perMonth := float64(r.spec.Width-12) / (float64(r.spec.Before+r.spec.After) / 30.44)
	step := 1
	if perMonth < 32 {
		step = 2 // label every other month on narrow ribbons
	}
	todayX := r.x(r.today)
	for ; !m.After(end); m = m.AddDate(0, 1, 0) {
		x := r.x(m)
		r.f(`<line class="rb-month" x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f"/>`, x, x, y0, y1)
		// On narrow ribbons label Jan, Mar, May… so January (the year) always shows.
		if !r.spec.Labels || (int(m.Month())-1)%step != 0 {
			continue
		}
		if step > 1 && todayX > x && todayX < x+22 {
			continue // keep the label off the today line
		}
		label := m.Format("Jan")
		if m.Month() == time.January {
			label = m.Format("2006") // the year marks where it turns over
		}
		r.f(`<text class="rb-label" x="%.1f" y="%.1f">%s</text>`, x+3, r.spec.LabelY, label)
	}
}

func (r *ribbon) today_(y0, y1 float64) {
	x := r.x(r.today)
	r.f(`<line class="rb-today" x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f"><title>Today, %s</title></line>`,
		x, x, y0, y1, r.today.Format("2 Jan 2006"))
}

func (r *ribbon) svg(label string) template.HTML {
	return template.HTML(fmt.Sprintf(
		`<svg class="ribbon" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="%s">%s</svg>`,
		r.spec.Width, r.spec.Height, r.spec.Width, r.spec.Height, esc(label), r.b.String()))
}

// axisSVG draws the month labels above the dashboard's ribbon column.
func axisSVG(today time.Time) template.HTML {
	r := newRibbon(axisRibbon, today)
	r.months(14, 26)
	x := r.x(today)
	r.f(`<line class="rb-today" x1="%.1f" x2="%.1f" y1="12" y2="26"/>`, x, x)
	r.f(`<text class="rb-label rb-label-today" x="%.1f" y="9" text-anchor="middle">today</text>`, x)
	return r.svg("Months from six months ago to nine months ahead")
}

// forecastSVG draws one asset's visits, due window, booking and projections.
func forecastSVG(f core.Forecast, spec ribbonSpec) template.HTML {
	r := newRibbon(spec, f.Today)
	top := 0.0
	if spec.Labels {
		top = 16
	}
	mid := top + (float64(spec.Height)-top)/2
	r.months(top, float64(spec.Height))
	r.f(`<line class="rb-base" x1="6" x2="%d" y1="%.1f" y2="%.1f"/>`, spec.Width-6, mid, mid)

	status := string(f.Status)
	if f.HasDue {
		ws, we := f.WindowStart(), f.WindowEnd()
		x0, x1 := r.x(ws), r.x(we)
		if x0 < 6 {
			x0 = 6
		}
		if right := float64(spec.Width - 6); x1 > right {
			x1 = right
		}
		if x1 > x0 {
			r.f(`<rect class="rb-window s-%s" x="%.1f" y="%.1f" width="%.1f" height="12" rx="6"><title>Booking window %s to %s</title></rect>`,
				status, x0, mid-6, x1-x0, ws.Format("2 Jan"), we.Format("2 Jan 2006"))
		}
	}
	r.today_(top, float64(spec.Height))

	for _, p := range f.Projections {
		if r.in(p) {
			r.f(`<circle class="rb-proj" cx="%.1f" cy="%.1f" r="3.5"><title>Projected %s</title></circle>`, r.x(p), mid, p.Format("2 Jan 2006"))
		}
	}
	for _, v := range f.Visits {
		if !r.in(v.Date) {
			continue
		}
		x := r.x(v.Date)
		tip := fmt.Sprintf("%s, %s", v.Status.Label(), v.Date.Format("2 Jan 2006"))
		switch v.Status {
		case core.VisitDone:
			r.f(`<circle class="rb-done" cx="%.1f" cy="%.1f" r="4.5"><title>%s</title></circle>`, x, mid, esc(tip))
		case core.VisitUnconfirmed:
			r.f(`<circle class="rb-unconfirmed" cx="%.1f" cy="%.1f" r="4.5"><title>%s</title></circle>`, x, mid, esc(tip))
		case core.VisitScheduled:
			r.f(`<circle class="rb-booked s-%s" cx="%.1f" cy="%.1f" r="5"><title>%s</title></circle>`, status, x, mid, esc(tip))
		case core.VisitMissed, core.VisitCancelled:
			cls := "rb-missed"
			if v.Status == core.VisitCancelled {
				cls = "rb-cancelled"
			}
			r.f(`<g class="%s"><title>%s</title><path d="M%.1f %.1fl7 7M%.1f %.1fl-7 7"/></g>`, cls, esc(tip), x-3.5, mid-3.5, x+3.5, mid-3.5)
		}
	}
	if f.HasDue && r.in(f.NextDue) {
		x := r.x(f.NextDue)
		r.f(`<path class="rb-due s-%s" d="M%.1f %.1fl6 6l-6 6l-6 -6z"><title>Next due %s</title></path>`,
			status, x, mid-6, f.NextDue.Format("2 Jan 2006"))
	}
	return r.svg(fmt.Sprintf("PM timeline for %s %s", f.Site.Code, f.Task.System))
}
