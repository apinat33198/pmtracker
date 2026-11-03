// Package gcal reads Google Calendar with a service account and keeps the
// local mirror in step using incremental sync tokens.
//
// It talks to the REST API with the standard library only: the service
// account signs a JWT (RS256), exchanges it for an access token, then pages
// through events.list.
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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/apinat33198/pmtracker/internal/core"
)

// maxPages caps one listing (250 events a page) in case paging never ends.
const maxPages = 400

// ReadOnlyScope is the narrowest scope that can list events on shared calendars.
const ReadOnlyScope = "https://www.googleapis.com/auth/calendar.events.readonly"

// ErrSyncTokenGone means the server invalidated the sync token (HTTP 410):
// the caller must wipe that calendar's mirror and do a full sync.
var ErrSyncTokenGone = errors.New("sync token invalidated by Google (410 Gone)")

// Source lists a calendar's events: a full listing when syncToken is empty
// (from timeMin onward), otherwise only changes since the token.
type Source interface {
	List(ctx context.Context, calendarID, syncToken string, timeMin time.Time) (events []core.Event, nextSyncToken string, err error)
}

type serviceAccount struct {
	Type         string `json:"type"`
	ClientEmail  string `json:"client_email"`
	PrivateKey   string `json:"private_key"`
	PrivateKeyID string `json:"private_key_id"`
	TokenURI     string `json:"token_uri"`
}

// Google is a Source backed by the Calendar API v3.
type Google struct {
	HTTP    *http.Client
	APIBase string // https://www.googleapis.com/calendar/v3
	Scope   string
	loc     *time.Location

	sa  serviceAccount
	key *rsa.PrivateKey

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// NewGoogle loads a service-account key file.
func NewGoogle(credentialsFile string, loc *time.Location) (*Google, error) {
	b, err := os.ReadFile(credentialsFile)
	if err != nil {
		return nil, fmt.Errorf("service account key: %w", err)
	}
	return NewGoogleFromJSON(b, loc)
}

func NewGoogleFromJSON(b []byte, loc *time.Location) (*Google, error) {
	var sa serviceAccount
	if err := json.Unmarshal(b, &sa); err != nil {
		return nil, fmt.Errorf("service account key: %w", err)
	}
	if sa.Type != "service_account" || sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, errors.New("service account key: not a service_account JSON key")
	}
	if sa.TokenURI == "" {
		sa.TokenURI = "https://oauth2.googleapis.com/token"
	}
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return nil, errors.New("service account key: private_key is not PEM")
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("service account key: private_key is not RSA")
		}
		key = rk
	} else if k, err2 := x509.ParsePKCS1PrivateKey(block.Bytes); err2 == nil {
		key = k
	} else {
		return nil, fmt.Errorf("service account key: %w", err)
	}
	return &Google{
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		APIBase: "https://www.googleapis.com/calendar/v3",
		Scope:   ReadOnlyScope,
		loc:     loc,
		sa:      sa,
		key:     key,
	}, nil
}

// ClientEmail is the address to share the PM calendar with.
func (g *Google) ClientEmail() string { return g.sa.ClientEmail }

// SetTokenURI overrides the OAuth token endpoint (tests).
func (g *Google) SetTokenURI(u string) { g.sa.TokenURI = u }

func (g *Google) accessToken(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.token != "" && time.Now().Before(g.tokenExp.Add(-time.Minute)) {
		return g.token, nil
	}
	now := time.Now()
	assertion, err := g.signJWT(now)
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.sa.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token exchange: HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("token exchange: unexpected response: %s", snippet(body))
	}
	g.token = tok.AccessToken
	g.tokenExp = now.Add(time.Duration(tok.ExpiresIn) * time.Second)
	return g.token, nil
}

func (g *Google) signJWT(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	if g.sa.PrivateKeyID != "" {
		header["kid"] = g.sa.PrivateKeyID
	}
	claims := map[string]any{
		"iss":   g.sa.ClientEmail,
		"scope": g.Scope,
		"aud":   g.sa.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	hb, _ := json.Marshal(header)
	cb, _ := json.Marshal(claims)
	signing := enc.EncodeToString(hb) + "." + enc.EncodeToString(cb)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, g.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

type apiTime struct {
	Date     string `json:"date"`
	DateTime string `json:"dateTime"`
}

type apiEvent struct {
	ID               string  `json:"id"`
	Status           string  `json:"status"`
	HTMLLink         string  `json:"htmlLink"`
	Summary          string  `json:"summary"`
	Description      string  `json:"description"`
	Location         string  `json:"location"`
	ICalUID          string  `json:"iCalUID"`
	RecurringEventID string  `json:"recurringEventId"`
	Updated          string  `json:"updated"`
	Start            apiTime `json:"start"`
	End              apiTime `json:"end"`
	Organizer        struct {
		Email string `json:"email"`
	} `json:"organizer"`
	Attendees []struct {
		Email string `json:"email"`
	} `json:"attendees"`
}

type apiList struct {
	Items         []apiEvent `json:"items"`
	NextPageToken string     `json:"nextPageToken"`
	NextSyncToken string     `json:"nextSyncToken"`
}

// List implements Source. Every request uses singleEvents=true so a
// recurring vendor series arrives as individual visits; the incremental
// request carries only syncToken (timeMin etc. are rejected with 400).
func (g *Google) List(ctx context.Context, calendarID, syncToken string, timeMin time.Time) ([]core.Event, string, error) {
	var out []core.Event
	pageToken := ""
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, "", fmt.Errorf("events.list: more than %d pages; stopping", maxPages)
		}
		q := url.Values{}
		q.Set("singleEvents", "true")
		q.Set("maxResults", "250")
		if syncToken != "" {
			q.Set("syncToken", syncToken)
		} else if !timeMin.IsZero() {
			q.Set("timeMin", timeMin.UTC().Format(time.RFC3339))
		}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		u := fmt.Sprintf("%s/calendars/%s/events?%s", strings.TrimRight(g.APIBase, "/"), url.PathEscape(calendarID), q.Encode())

		tok, err := g.accessToken(ctx)
		if err != nil {
			return nil, "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, "", err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := g.HTTP.Do(req)
		if err != nil {
			return nil, "", fmt.Errorf("events.list: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusGone:
			return nil, "", ErrSyncTokenGone
		case resp.StatusCode == http.StatusNotFound:
			return nil, "", fmt.Errorf("calendar %q not found: share it with %s (See all event details)", calendarID, g.sa.ClientEmail)
		case resp.StatusCode != http.StatusOK:
			return nil, "", fmt.Errorf("events.list: HTTP %d: %s", resp.StatusCode, snippet(body))
		}
		var page apiList
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, "", fmt.Errorf("events.list: %w", err)
		}
		for _, it := range page.Items {
			out = append(out, g.convert(calendarID, it))
		}
		if page.NextPageToken != "" {
			pageToken = page.NextPageToken
			continue
		}
		return out, page.NextSyncToken, nil
	}
}

func (g *Google) convert(calendarID string, it apiEvent) core.Event {
	e := core.Event{
		CalendarID:       calendarID,
		ID:               it.ID,
		ICalUID:          it.ICalUID,
		RecurringEventID: it.RecurringEventID,
		Status:           it.Status,
		Summary:          it.Summary,
		Description:      it.Description,
		Location:         it.Location,
		Organizer:        it.Organizer.Email,
		HTMLLink:         it.HTMLLink,
	}
	for _, a := range it.Attendees {
		if a.Email != "" {
			e.Attendees = append(e.Attendees, a.Email)
		}
	}
	e.Start, e.AllDay = parseAPITime(it.Start, g.loc)
	e.End, _ = parseAPITime(it.End, g.loc)
	if t, err := time.Parse(time.RFC3339, it.Updated); err == nil {
		e.Updated = t
	}
	return e
}

func parseAPITime(t apiTime, loc *time.Location) (time.Time, bool) {
	if t.Date != "" {
		d, err := time.ParseInLocation("2006-01-02", t.Date, loc)
		if err == nil {
			return d, true
		}
	}
	if t.DateTime != "" {
		d, err := time.Parse(time.RFC3339, t.DateTime)
		if err == nil {
			return d.In(loc), false
		}
	}
	return time.Time{}, false
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
