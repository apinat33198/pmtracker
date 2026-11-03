package gcal

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apinat33198/pmtracker/internal/config"
	"github.com/apinat33198/pmtracker/internal/core"
	"github.com/apinat33198/pmtracker/internal/store"
)

func fakeGoogle(t *testing.T) (*Google, *httptest.Server, *atomic.Int32) {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	var tokenCalls atomic.Int32
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)

	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		tokenCalls.Add(1)
		b, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(b))
		if form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			http.Error(w, "bad grant", 400)
			return
		}
		parts := strings.Split(form.Get("assertion"), ".")
		if len(parts) != 3 {
			http.Error(w, "bad jwt", 400)
			return
		}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
			http.Error(w, "bad signature", 401)
			return
		}
		cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var claims map[string]any
		json.Unmarshal(cb, &claims)
		if claims["scope"] != ReadOnlyScope || claims["iss"] != "pm-reader@proj.iam.gserviceaccount.com" || claims["aud"] != srv.URL+"/token" {
			http.Error(w, "bad claims", 401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at-123", "expires_in": 3600, "token_type": "Bearer"})
	})

	mux.HandleFunc("GET /calendar/v3/calendars/{cal}/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-123" {
			http.Error(w, "unauthorized", 401)
			return
		}
		q := r.URL.Query()
		if q.Get("singleEvents") != "true" {
			http.Error(w, "singleEvents missing", 400)
			return
		}
		if q.Get("syncToken") != "" && q.Get("timeMin") != "" {
			http.Error(w, "syncToken cannot be combined with timeMin", 400)
			return
		}
		switch {
		case q.Get("syncToken") == "" && q.Get("pageToken") == "":
			json.NewEncoder(w).Encode(map[string]any{
				"items": []any{map[string]any{
					"id": "e1", "status": "confirmed", "summary": "[PM] UPS-01 – ABC Power",
					"start": map[string]string{"date": "2026-11-20"}, "end": map[string]string{"date": "2026-11-21"},
				}},
				"nextPageToken": "p2",
			})
		case q.Get("pageToken") == "p2":
			json.NewEncoder(w).Encode(map[string]any{
				"items": []any{map[string]any{
					"id": "e2", "status": "confirmed", "summary": "Lift visit", "recurringEventId": "s1",
					"start":     map[string]string{"dateTime": "2026-12-01T03:00:00Z"},
					"end":       map[string]string{"dateTime": "2026-12-01T05:00:00Z"},
					"organizer": map[string]string{"email": "planner@liftco.example"},
					"attendees": []map[string]string{{"email": "a@x.example"}},
				}},
				"nextSyncToken": "sync-A",
			})
		case q.Get("syncToken") == "sync-A":
			json.NewEncoder(w).Encode(map[string]any{
				"items":         []any{map[string]any{"id": "e1", "status": "cancelled"}},
				"nextSyncToken": "sync-B",
			})
		case q.Get("syncToken") == "sync-B":
			w.WriteHeader(http.StatusGone)
			w.Write([]byte(`{"error":{"code":410,"message":"Sync token is no longer valid"}}`))
		default:
			http.Error(w, "unexpected", 500)
		}
	})
	t.Cleanup(srv.Close)

	sa, _ := json.Marshal(map[string]string{
		"type": "service_account", "client_email": "pm-reader@proj.iam.gserviceaccount.com",
		"private_key": pemKey, "private_key_id": "k1", "token_uri": srv.URL + "/token",
	})
	loc, _ := time.LoadLocation("Asia/Bangkok")
	g, err := NewGoogleFromJSON(sa, loc)
	if err != nil {
		t.Fatal(err)
	}
	g.APIBase = srv.URL + "/calendar/v3"
	return g, srv, &tokenCalls
}

func TestGoogleListAndSyncer(t *testing.T) {
	g, _, tokenCalls := fakeGoogle(t)
	ctx := context.Background()

	evs, next, err := g.List(ctx, "pm@group.calendar.google.com", "", time.Now().AddDate(-2, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || next != "sync-A" {
		t.Fatalf("full sync = %d events, token %q", len(evs), next)
	}
	if !evs[0].AllDay || evs[0].Start.Format("2006-01-02 15:04 MST") != "2026-11-20 00:00 +07" {
		t.Errorf("all-day start = %v", evs[0].Start)
	}
	if evs[1].AllDay || evs[1].Start.Format("15:04") != "10:00" || evs[1].RecurringEventID != "s1" || len(evs[1].Attendees) != 1 {
		t.Errorf("timed event = %+v", evs[1])
	}
	if tokenCalls.Load() != 1 {
		t.Errorf("token should be cached across pages, got %d exchanges", tokenCalls.Load())
	}

	// Run the syncer end-to-end: full → incremental (deletion) → 410 → full again.
	cfg, err := config.Parse([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open(filepath.Join(t.TempDir(), "d.json"))
	if err := st.UpdateCatalog("test", "Add calendar", func(c *core.Catalog) error {
		c.Calendars = []core.Calendar{{ID: "pm@group.calendar.google.com", Label: "PM"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sy := &Syncer{Store: st, Source: g, Cfg: cfg, Now: time.Now, Log: log.New(io.Discard, "", 0)}

	r := sy.SyncAll(ctx)[0]
	if r.Err != nil || !r.Full || r.Changes != 2 {
		t.Fatalf("first sync = %+v", r)
	}
	r = sy.SyncAll(ctx)[0]
	if r.Err != nil || r.Full || r.Changes != 1 {
		t.Fatalf("incremental = %+v", r)
	}
	for _, e := range st.Inputs().Events {
		if e.ID == "e1" && (e.Status != "cancelled" || e.Summary == "") {
			t.Errorf("deleted event should be kept as cancelled with its title: %+v", e)
		}
	}
	r = sy.SyncAll(ctx)[0] // token sync-B → 410 → full resync
	if r.Err != nil || !r.Full {
		t.Fatalf("410 recovery = %+v", r)
	}
	if got := st.CalendarState("pm@group.calendar.google.com").SyncToken; got != "sync-A" {
		t.Errorf("token after recovery = %q, want sync-A", got)
	}
}

func TestGoogleNotShared(t *testing.T) {
	g, srv, _ := fakeGoogle(t)
	g.APIBase = srv.URL + "/missing"
	_, _, err := g.List(context.Background(), "x", "", time.Time{})
	if err == nil || !strings.Contains(err.Error(), "pm-reader@proj.iam.gserviceaccount.com") {
		t.Errorf("want a hint to share the calendar with the service account, got %v", err)
	}
}

// goneThenDown answers the first full listing, then says the sync token is
// gone and fails the re-read.
type goneThenDown struct{ calls int }

func (f *goneThenDown) List(_ context.Context, _, tok string, _ time.Time) ([]core.Event, string, error) {
	f.calls++
	if tok != "" {
		return nil, "", ErrSyncTokenGone
	}
	if f.calls == 1 {
		return []core.Event{{ID: "a", Status: "confirmed", Summary: "[PM] BKK1 UPS", Start: time.Now()}}, "t1", nil
	}
	return nil, "", errors.New("network down")
}

func TestFailedResyncKeepsTheMirror(t *testing.T) {
	cfg, _ := config.Parse([]byte(`{}`))
	st, _ := store.Open(filepath.Join(t.TempDir(), "d.json"))
	st.UpdateCatalog("test", "Add calendar", func(c *core.Catalog) error {
		c.Calendars = []core.Calendar{{ID: "pm", Label: "PM"}}
		return nil
	})
	sy := &Syncer{Store: st, Source: &goneThenDown{}, Cfg: cfg, Now: time.Now, Log: log.New(io.Discard, "", 0)}
	sy.SyncAll(context.Background())
	if r := sy.SyncAll(context.Background())[0]; r.Err == nil {
		t.Fatal("the re-read should fail")
	}
	if n := len(st.Inputs().Events); n != 1 {
		t.Errorf("a failed re-read after 410 emptied the mirror: %d events", n)
	}
}
