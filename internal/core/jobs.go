package core

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Job is a one-off piece of work at a site, such as a repair to the raised
// floor, outside the PM cycle. A job has no interval and no forecast, and
// never changes a PM task's status.
type Job struct {
	ID      int       `json:"id"`
	SiteID  string    `json:"site_id"`
	Title   string    `json:"title"`
	Date    string    `json:"date"`          // YYYY-MM-DD: when it's planned or needed
	Who     string    `json:"who,omitempty"` // vendor, contractor or person doing it
	Note    string    `json:"note,omitempty"`
	Status  string    `json:"status"`            // open | done
	DoneOn  string    `json:"done_on,omitempty"` // YYYY-MM-DD
	DoneBy  string    `json:"done_by,omitempty"`
	By      string    `json:"by,omitempty"`
	Created time.Time `json:"created"`
}

// Job statuses.
const (
	JobOpen = "open"
	JobDone = "done"
)

// Limits on a job's text, so one form can't bloat the data file.
const (
	MaxJobTitle = 200
	MaxJobWho   = 100
	MaxJobNote  = 2000
)

// Clean tidies a job's text and checks it: a site, a title and a date are
// needed; a done job needs the date it was done.
func (j *Job) Clean() error {
	j.Title = oneLine(j.Title)
	j.Who = oneLine(j.Who)
	j.Note = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, strings.ReplaceAll(j.Note, "\r\n", "\n")))
	switch {
	case j.SiteID == "":
		return errors.New("pick the site")
	case j.Title == "":
		return errors.New("enter what the job is")
	case utf8.RuneCountInString(j.Title) > MaxJobTitle:
		return fmt.Errorf("keep the job title under %d characters", MaxJobTitle)
	case utf8.RuneCountInString(j.Who) > MaxJobWho:
		return fmt.Errorf("keep who does it under %d characters", MaxJobWho)
	case utf8.RuneCountInString(j.Note) > MaxJobNote:
		return fmt.Errorf("keep the note under %d characters", MaxJobNote)
	}
	if _, err := time.Parse("2006-01-02", j.Date); err != nil {
		return errors.New("enter the job's date")
	}
	switch j.Status {
	case "", JobOpen:
		j.Status, j.DoneOn, j.DoneBy = JobOpen, "", ""
	case JobDone:
		if _, err := time.Parse("2006-01-02", j.DoneOn); err != nil {
			return errors.New("enter the date the job was done")
		}
	default:
		return fmt.Errorf("unknown job status %q", j.Status)
	}
	return nil
}

// oneLine trims s and turns runs of spaces, tabs and line breaks into one space.
func oneLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
}

// JobView is a job with its site and dates worked out for display.
type JobView struct {
	Job
	Site     Site
	Day      time.Time // Date, local midnight
	Done     time.Time // DoneOn, local midnight (zero while open)
	DaysLeft int       // days from today to Day; negative is late
}

func (j JobView) IsOpen() bool { return j.Status != JobDone }

// Tone is the status colour a job borrows from PM: late is red, today is
// amber, later is blue and done is green.
func (j JobView) Tone() Status {
	switch {
	case !j.IsOpen():
		return StatusOnTrack
	case j.DaysLeft < 0:
		return StatusOverdue
	case j.DaysLeft == 0:
		return StatusDueSoon
	}
	return StatusBooked
}

// Label names the job's state.
func (j JobView) Label() string {
	switch j.Tone() {
	case StatusOnTrack:
		return "Done"
	case StatusOverdue:
		return "Late"
	case StatusDueSoon:
		return "Today"
	}
	return "Open"
}

// buildJobs splits jobs into open ones, soonest first, and done ones, most
// recently done first. Jobs at archived or unknown sites are left out, as
// their PM tasks are.
func buildJobs(cat *Catalog, jobs []Job, today time.Time, loc *time.Location) (open, done []JobView) {
	for _, j := range jobs {
		site, ok := cat.Site(j.SiteID)
		if !ok || site.Archived {
			continue
		}
		day, err := time.ParseInLocation("2006-01-02", j.Date, loc)
		if err != nil {
			continue
		}
		v := JobView{Job: j, Site: site, Day: day, DaysLeft: DaysBetween(today, day)}
		if j.Status == JobDone {
			v.Done, _ = time.ParseInLocation("2006-01-02", j.DoneOn, loc)
			done = append(done, v)
		} else {
			open = append(open, v)
		}
	}
	sort.SliceStable(open, func(a, b int) bool {
		x, y := open[a], open[b]
		if !x.Day.Equal(y.Day) {
			return x.Day.Before(y.Day)
		}
		if x.Site.Code != y.Site.Code {
			return x.Site.Code < y.Site.Code
		}
		return x.ID < y.ID
	})
	sort.SliceStable(done, func(a, b int) bool {
		x, y := done[a], done[b]
		if !x.Done.Equal(y.Done) {
			return x.Done.After(y.Done)
		}
		return x.ID > y.ID
	})
	return open, done
}

// JobsAt returns a site's open and done jobs.
func (s Snapshot) JobsAt(siteID string) (open, done []JobView) {
	for _, j := range s.OpenJobs {
		if j.SiteID == siteID {
			open = append(open, j)
		}
	}
	for _, j := range s.DoneJobs {
		if j.SiteID == siteID {
			done = append(done, j)
		}
	}
	return open, done
}

// Job finds a job by ID.
func (s Snapshot) Job(id int) (JobView, bool) {
	for _, list := range [][]JobView{s.OpenJobs, s.DoneJobs} {
		for _, j := range list {
			if j.ID == id {
				return j, true
			}
		}
	}
	return JobView{}, false
}
