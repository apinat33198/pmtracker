package web

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/apinat33198/pmtracker/internal/core"
	"github.com/apinat33198/pmtracker/internal/store"
)

// Ad-hoc jobs: one-off work at a site (a repair, an inspection) kept apart
// from the PM tasks. They never change a PM forecast or status.

type jobsData struct {
	Open  []core.JobView
	Done  []core.JobView
	More  int // done jobs not shown
	Sites []core.Site
	Who   []string
	Today time.Time
}

// jobsDoneShown is how many done jobs the Jobs page lists; the CSV has all.
const jobsDoneShown = 100

func (s *Server) jobsPage(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	d := jobsData{Open: snap.OpenJobs, Done: snap.DoneJobs, Today: snap.Today, Sites: liveSites(snap), Who: whoOptions(snap)}
	if len(d.Done) > jobsDoneShown {
		d.More = len(d.Done) - jobsDoneShown
		d.Done = d.Done[:jobsDoneShown]
	}
	s.render(w, r, "jobs", "jobs", "Ad-hoc jobs", snap, d)
}

// liveSites are the sites a job can be added to, in overview order.
func liveSites(snap core.Snapshot) []core.Site {
	var out []core.Site
	for _, ss := range snap.Sites {
		out = append(out, ss.Site)
	}
	return out
}

// whoOptions suggests who does a job: the vendor list, then names used on
// earlier jobs.
func whoOptions(snap core.Snapshot) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if k := strings.ToLower(name); name != "" && !seen[k] {
			seen[k] = true
			out = append(out, name)
		}
	}
	for _, v := range snap.Catalog.Vendors {
		add(v.Name)
	}
	var used []string
	for _, list := range [][]core.JobView{snap.OpenJobs, snap.DoneJobs} {
		for _, j := range list {
			used = append(used, j.Who)
		}
	}
	sort.Strings(used)
	for _, w := range used {
		add(w)
	}
	return out
}

func jobLabel(cat core.Catalog, j core.Job) string {
	code := j.SiteID
	if site, ok := cat.Site(j.SiteID); ok {
		code = site.Code
	}
	return fmt.Sprintf("“%s” at %s", j.Title, code)
}

func (s *Server) addJob(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	j := core.Job{SiteID: r.FormValue("site"), Title: r.FormValue("title"), Date: r.FormValue("date"), Who: r.FormValue("who"),
		Note: r.FormValue("note"), Status: core.JobOpen, By: u.Name, Created: s.Now()}
	cat := s.Store.Catalog()
	if site, ok := cat.Site(j.SiteID); !ok || site.Archived {
		s.back(w, r, "/jobs", "Pick the site the job is at.", "error")
		return
	}
	if err := j.Clean(); err != nil {
		s.back(w, r, "/jobs", "The job wasn't added: "+err.Error()+".", "error")
		return
	}
	day, _ := time.Parse("2006-01-02", j.Date)
	j, err := s.Store.AddJob(j, fmt.Sprintf("Added the ad-hoc job %s for %s", jobLabel(cat, j), day.Format("2 Jan 2006")))
	if err != nil {
		s.back(w, r, "/jobs", "The job wasn't added: "+err.Error()+".", "error")
		return
	}
	s.back(w, r, "/jobs", fmt.Sprintf("Added %s.", jobLabel(cat, j)), "")
}

// jobID reads the job number from the address.
func jobID(r *http.Request) int {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id < 1 {
		return 0
	}
	return id
}

// changeJob runs one change to a job and reports the outcome.
func (s *Server) changeJob(w http.ResponseWriter, r *http.Request, verb, done string, fn func(*core.Job) error) {
	cat := s.Store.Catalog()
	j, err := s.Store.UpdateJob(jobID(r), userOf(r).Name, func(j core.Job) string {
		return verb + " the ad-hoc job " + jobLabel(cat, j)
	}, fn)
	switch {
	case errors.Is(err, store.ErrNoJob):
		s.back(w, r, "/jobs", "That job no longer exists.", "error")
	case err != nil:
		s.back(w, r, "/jobs", "Nothing was changed: "+err.Error()+".", "error")
	default:
		s.back(w, r, "/jobs", fmt.Sprintf(done, jobLabel(cat, j)), "")
	}
}

func (s *Server) editJob(w http.ResponseWriter, r *http.Request) {
	s.changeJob(w, r, "Edited", "Saved %s.", func(j *core.Job) error {
		j.Title, j.Date, j.Who, j.Note = r.FormValue("title"), r.FormValue("date"), r.FormValue("who"), r.FormValue("note")
		if j.Status == core.JobDone {
			j.DoneOn = r.FormValue("done_on")
			if d, err := time.ParseInLocation("2006-01-02", j.DoneOn, s.Cfg.Location()); err == nil && d.After(s.today()) {
				return errors.New("the date it was done can't be in the future")
			}
		}
		return nil
	})
}

func (s *Server) jobDone(w http.ResponseWriter, r *http.Request) {
	by := userOf(r).Name
	s.changeJob(w, r, "Marked as done", "Marked %s as done.", func(j *core.Job) error {
		if j.Status == core.JobDone {
			return errors.New("it's already done")
		}
		j.Status, j.DoneOn, j.DoneBy = core.JobDone, s.today().Format("2006-01-02"), by
		return nil
	})
}

func (s *Server) jobReopen(w http.ResponseWriter, r *http.Request) {
	s.changeJob(w, r, "Reopened", "Reopened %s.", func(j *core.Job) error {
		if j.Status != core.JobDone {
			return errors.New("it's still open")
		}
		j.Status = core.JobOpen
		return nil
	})
}

func (s *Server) deleteJob(w http.ResponseWriter, r *http.Request) {
	cat := s.Store.Catalog()
	j, err := s.Store.DeleteJob(jobID(r), userOf(r).Name, func(j core.Job) string { return "Deleted the ad-hoc job " + jobLabel(cat, j) })
	switch {
	case errors.Is(err, store.ErrNoJob):
		s.back(w, r, "/jobs", "That job no longer exists.", "error")
	case err != nil:
		s.fail(w, err)
	default:
		s.back(w, r, "/jobs", fmt.Sprintf("Deleted %s.", jobLabel(cat, j)), "")
	}
}

func (s *Server) today() time.Time { return core.Day(s.Now(), s.Cfg.Location()) }

func (s *Server) jobsCSV(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="adhoc-jobs-%s.csv"`, snap.Today.Format("2006-01-02")))
	w.Write([]byte("\ufeff")) // so Excel opens Thai text as UTF-8
	cw := csv.NewWriter(w)
	cw.Write([]string{"site_code", "site_name", "region", "job", "date", "who", "status", "done_on", "done_by", "note", "added_by", "added_at"})
	for _, list := range [][]core.JobView{snap.OpenJobs, snap.DoneJobs} {
		for _, j := range list {
			cw.Write([]string{csvCell(j.Site.Code), csvCell(j.Site.Name), csvCell(j.Site.Region), csvCell(j.Title), j.Date, csvCell(j.Who),
				j.Label(), j.DoneOn, csvCell(j.DoneBy), csvCell(j.Note), csvCell(j.By), j.Created.In(s.Cfg.Location()).Format("2006-01-02 15:04")})
		}
	}
	cw.Flush()
}
