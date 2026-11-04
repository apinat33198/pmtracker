package core

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestJobClean(t *testing.T) {
	j := Job{SiteID: "bkk1", Title: "  Repair\n raised\tfloor ", Who: " Siam  Floor ", Note: " line one\r\nline two\x07 ", Date: "2026-11-04"}
	if err := j.Clean(); err != nil {
		t.Fatal(err)
	}
	if j.Title != "Repair raised floor" || j.Who != "Siam Floor" || j.Note != "line one\nline two" || j.Status != JobOpen {
		t.Errorf("cleaned = %+v", j)
	}
	bad := []Job{
		{Title: "x", Date: "2026-11-04"},                                                // no site
		{SiteID: "bkk1", Title: "   ", Date: "2026-11-04"},                              // no title
		{SiteID: "bkk1", Title: "x", Date: "4/11/2026"},                                 // bad date
		{SiteID: "bkk1", Title: strings.Repeat("ก", MaxJobTitle+1), Date: "2026-11-04"}, // too long
		{SiteID: "bkk1", Title: "x", Date: "2026-11-04", Status: JobDone},               // done, no date
		{SiteID: "bkk1", Title: "x", Date: "2026-11-04", Status: "maybe"},               // unknown status
	}
	for _, b := range bad {
		if err := b.Clean(); err == nil {
			t.Errorf("%+v should be refused", b)
		}
	}
	// A title of exactly the limit in Thai (3 bytes a letter) is fine.
	ok := Job{SiteID: "bkk1", Title: strings.Repeat("ก", MaxJobTitle), Date: "2026-11-04"}
	if err := ok.Clean(); err != nil {
		t.Error(err)
	}
	// Reopening clears when and by whom it was done.
	re := Job{SiteID: "bkk1", Title: "x", Date: "2026-11-04", Status: JobOpen, DoneOn: "2026-11-05", DoneBy: "nok"}
	if re.Clean(); re.DoneOn != "" || re.DoneBy != "" {
		t.Errorf("reopened = %+v", re)
	}
}

func TestBuildJobs(t *testing.T) {
	c := cfg(t)
	now := date(t, c, "2026-11-04").Add(16 * time.Hour)
	in := Inputs{Catalog: testCatalog(), Jobs: []Job{
		{ID: 1, SiteID: "cnx1", Title: "Fix door reader", Date: "2026-11-09", Status: JobOpen},
		{ID: 2, SiteID: "bkk1", Title: "Repair raised floor", Date: "2026-11-04", Who: "Siam Floor Works", Status: JobOpen},
		{ID: 3, SiteID: "bkk1", Title: "Replace tiles", Date: "2026-11-01", Status: JobOpen},
		{ID: 4, SiteID: "old", Title: "At an archived site", Date: "2026-11-01", Status: JobOpen},
		{ID: 5, SiteID: "gone", Title: "At a deleted site", Date: "2026-11-01", Status: JobOpen},
		{ID: 6, SiteID: "bkk1", Title: "Drain", Date: "2026-10-20", Status: JobDone, DoneOn: "2026-10-21"},
		{ID: 7, SiteID: "cnx1", Title: "Lamp", Date: "2026-10-01", Status: JobDone, DoneOn: "2026-10-30"},
	}}
	snap := Build(c, in, now)
	var ids []int
	var labels []string
	for _, j := range snap.OpenJobs {
		ids = append(ids, j.ID)
		labels = append(labels, j.Label())
	}
	if !reflect.DeepEqual(ids, []int{3, 2, 1}) || !reflect.DeepEqual(labels, []string{"Late", "Today", "Open"}) {
		t.Errorf("open = %v %v, want late, today, then later; none at archived or deleted sites", ids, labels)
	}
	if j := snap.OpenJobs[0]; j.DaysLeft != -3 || j.Tone() != StatusOverdue || j.Site.Code != "BKK1" {
		t.Errorf("late job = %+v", j)
	}
	if len(snap.DoneJobs) != 2 || snap.DoneJobs[0].ID != 7 || snap.DoneJobs[0].Label() != "Done" {
		t.Errorf("done = %+v, want the most recently done first", snap.DoneJobs)
	}
	open, done := snap.JobsAt("bkk1")
	if len(open) != 2 || len(done) != 1 {
		t.Errorf("bkk1 has %d open and %d done", len(open), len(done))
	}
	if _, ok := snap.Job(6); !ok {
		t.Error("Job(6) should find the done job")
	}
	// Jobs never touch PM: the same build without them gives the same forecasts.
	in.Jobs = nil
	if plain := Build(c, in, now); !reflect.DeepEqual(plain.Forecasts, snap.Forecasts) || !reflect.DeepEqual(plain.Counts, snap.Counts) {
		t.Error("ad-hoc jobs must not change PM forecasts")
	}

	d := Digest(snap, "")
	if !strings.Contains(d, "Ad-hoc jobs due (2)") || !strings.Contains(d, "• BKK1 Replace tiles: 3 days late") ||
		!strings.Contains(d, "• BKK1 Repair raised floor: today (Siam Floor Works)") || strings.Contains(d, "door reader") {
		t.Errorf("digest should list jobs due today or late, not later ones:\n%s", d)
	}
}
