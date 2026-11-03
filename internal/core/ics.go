package core

import (
	"fmt"
	"strings"
	"time"

	"github.com/apinat33198/pmtracker/internal/config"
)

// ForecastICS renders projected due dates as an iCalendar feed that Google
// Calendar can subscribe to ("Other calendars" → "From URL"). The app stays
// read-only towards Google: Google pulls this feed on its own schedule.
func ForecastICS(cfg *config.Config, s Snapshot, now time.Time) string {
	var b strings.Builder
	w := func(line string) { b.WriteString(foldICS(line)); b.WriteString("\r\n") }
	stamp := now.UTC().Format("20060102T150405Z")

	w("BEGIN:VCALENDAR")
	w("VERSION:2.0")
	w("PRODID:-//pmtracker//PM forecast//EN")
	w("CALSCALE:GREGORIAN")
	w("METHOD:PUBLISH")
	w("X-WR-CALNAME:PM forecast")
	w("X-WR-TIMEZONE:" + cfg.Timezone)

	event := func(uid string, day time.Time, summary, desc string) {
		w("BEGIN:VEVENT")
		w("UID:" + uid)
		w("DTSTAMP:" + stamp)
		w("DTSTART;VALUE=DATE:" + day.Format("20060102"))
		w("DTEND;VALUE=DATE:" + day.AddDate(0, 0, 1).Format("20060102"))
		w("SUMMARY:" + escapeICS(summary))
		if desc != "" {
			w("DESCRIPTION:" + escapeICS(desc))
		}
		w("TRANSP:TRANSPARENT")
		w("END:VEVENT")
	}

	for _, f := range s.Forecasts {
		if !f.HasDue {
			continue
		}
		label := s.Catalog.TaskLabel(f.Task.ID)
		desc := fmt.Sprintf("%s\nStatus: %s\nWindow: %s to %s", f.Site.Name, f.Status.Label(),
			f.WindowStart().Format("2 Jan 2006"), f.WindowEnd().Format("2 Jan 2006"))
		if f.Booked != nil {
			desc += "\nBooked: " + f.Booked.Date.Format("Mon 2 Jan 2006")
		}
		if f.LastDone != nil {
			desc += "\nLast PM: " + f.LastDone.Date.Format("2 Jan 2006")
		}
		event(fmt.Sprintf("pm-due-%s-%s@pmtracker", f.Task.ID, f.NextDue.Format("20060102")),
			f.NextDue, "PM due: "+label, desc)
		for _, p := range f.Projections {
			event(fmt.Sprintf("pm-proj-%s-%s@pmtracker", f.Task.ID, p.Format("20060102")),
				p, "PM projected: "+label, "Projected from the current cycle.")
		}
	}
	w("END:VCALENDAR")
	return b.String()
}

func escapeICS(s string) string {
	r := strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`)
	return r.Replace(s)
}

// foldICS folds lines longer than 75 octets (RFC 5545 §3.1) without
// splitting a UTF-8 character.
func foldICS(line string) string {
	if len(line) <= 75 {
		return line
	}
	var b strings.Builder
	limit := 75
	for len(line) > limit {
		cut := limit
		for cut > 0 && line[cut]&0xC0 == 0x80 { // inside a multi-byte rune
			cut--
		}
		b.WriteString(line[:cut])
		b.WriteString("\r\n ")
		line = line[cut:]
		limit = 74 // continuation lines start with a space
	}
	b.WriteString(line)
	return b.String()
}
