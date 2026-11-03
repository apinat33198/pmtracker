package gcal

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/apinat33198/pmtracker/internal/config"
	"github.com/apinat33198/pmtracker/internal/store"
)

// Syncer mirrors every configured calendar into the store.
type Syncer struct {
	Store  *store.Store
	Source Source
	Cfg    *config.Config
	Now    func() time.Time
	Log    *log.Logger

	mu sync.Mutex
}

type Result struct {
	Calendar string
	Full     bool
	Changes  int
	Err      error
}

// SyncAll syncs each calendar in turn, waiting for any run in progress.
func (s *Syncer) SyncAll(ctx context.Context) []Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncAll(ctx)
}

// TrySyncAll skips the run when another one is in progress.
func (s *Syncer) TrySyncAll(ctx context.Context) ([]Result, bool) {
	if !s.mu.TryLock() {
		return nil, false
	}
	defer s.mu.Unlock()
	return s.syncAll(ctx), true
}

func (s *Syncer) syncAll(ctx context.Context) []Result {
	var out []Result
	for _, cal := range s.Store.Catalog().Calendars {
		r := s.syncOne(ctx, cal.ID)
		if r.Err != nil {
			s.Log.Printf("sync %s: %v", cal.ID, r.Err)
		} else if r.Changes > 0 || r.Full {
			s.Log.Printf("sync %s: full=%v changes=%d", cal.ID, r.Full, r.Changes)
		}
		out = append(out, r)
	}
	return out
}

func (s *Syncer) syncOne(ctx context.Context, calID string) Result {
	now := s.Now()
	st := s.Store.CalendarState(calID)
	full := st.SyncToken == ""
	timeMin := now.AddDate(0, -s.Cfg.SyncLookbackMonths, 0)

	events, next, err := s.Source.List(ctx, calID, st.SyncToken, timeMin)
	if errors.Is(err, ErrSyncTokenGone) {
		// Re-read everything. The mirror is replaced only once the full
		// listing has arrived, so a failure here leaves the old copy in place.
		s.Log.Printf("sync %s: token invalidated, running a full sync", calID)
		full = true
		events, next, err = s.Source.List(ctx, calID, "", timeMin)
	}
	if err != nil {
		_ = s.Store.SetSyncError(calID, err, now)
		return Result{Calendar: calID, Full: full, Err: err}
	}
	n, err := s.Store.ApplySync(calID, full, events, next, now)
	return Result{Calendar: calID, Full: full, Changes: n, Err: err}
}

// Run syncs immediately and then every interval until ctx ends.
func (s *Syncer) Run(ctx context.Context, every time.Duration) {
	s.TrySyncAll(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.TrySyncAll(ctx)
		}
	}
}
