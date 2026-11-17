package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/apinat33198/pmtracker/internal/core"
)

func TestQuietSyncDoesNotRewriteTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	s, _ := Open(path)
	s.UpdateCatalog("t", "", func(c *core.Catalog) error { c.Calendars = []core.Calendar{{ID: "cal", Label: "PM"}}; return nil })
	ev := []core.Event{{ID: "a", Status: "confirmed", Summary: "[PM] BKK1 UPS", Start: day("2026-11-20"), AllDay: true}}
	s.ApplySync("cal", true, ev, "tok1", day("2026-11-03"))
	before, _ := os.Stat(path)
	time.Sleep(20 * time.Millisecond)
	if n, err := s.ApplySync("cal", false, nil, "tok2", day("2026-11-04")); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	after, _ := os.Stat(path)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("a sync with no changes rewrote the data file")
	}
	if st := s.CalendarState("cal"); st.SyncToken != "tok2" || !st.LastSyncedAt.Equal(day("2026-11-04")) {
		t.Errorf("in memory: %+v", st)
	}
	// The same failure twice writes once; a change writes again.
	s.SetSyncError("cal", errors.New("403"), day("2026-11-05"))
	mid, _ := os.Stat(path)
	time.Sleep(20 * time.Millisecond)
	s.SetSyncError("cal", errors.New("403"), day("2026-11-06"))
	if again, _ := os.Stat(path); !again.ModTime().Equal(mid.ModTime()) {
		t.Error("the same sync error rewrote the file")
	}
	ev[0].Start = day("2026-11-21")
	s.ApplySync("cal", false, ev, "tok3", day("2026-11-07"))
	s2, _ := Open(path)
	if st := s2.CalendarState("cal"); st.SyncToken != "tok3" || st.LastError != "" {
		t.Errorf("after a sync with changes, on disk: %+v", st)
	}
}
