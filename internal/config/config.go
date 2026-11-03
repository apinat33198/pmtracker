// Package config loads the tracker's JSON configuration: where to listen,
// Google credentials, default PM cycle settings, feeds, digest and users.
//
// Sites, systems, vendors, PM tasks and calendars are managed in the admin
// pages and stored in the data file, not here. Calendars listed in this file
// only seed an empty data file on first run.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Roles, lowest to highest.
const (
	RoleViewer = "viewer" // read everything, export
	RoleEditor = "editor" // also review inbox, confirm visits, add past visits
	RoleAdmin  = "admin"  // also manage sites, tasks, vendors, systems, calendars
)

type Config struct {
	Timezone            string         `json:"timezone"`
	Listen              string         `json:"listen"`
	PublicURL           string         `json:"public_url"`
	DataFile            string         `json:"data_file"`
	SyncIntervalMinutes int            `json:"sync_interval_minutes"`
	SyncLookbackMonths  int            `json:"sync_lookback_months"`
	Google              Google         `json:"google"`
	Defaults            Defaults       `json:"defaults"`
	Keywords            []string       `json:"keywords"`
	Holidays            []string       `json:"holidays"`
	Feed                Feed           `json:"feed"`
	Digest              Digest         `json:"digest"`
	Auth                Auth           `json:"auth"`
	SeedCalendars       []SeedCalendar `json:"calendars"`

	loc      *time.Location
	holidays map[string]bool
}

type Google struct {
	CredentialsFile string `json:"credentials_file"`
}

type Defaults struct {
	IntervalMonths int `json:"interval_months"`
	WindowDays     int `json:"window_days"`
	LeadDays       int `json:"lead_days"`
	GraceDays      int `json:"grace_days"`
}

type SeedCalendar struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	PMOnly bool   `json:"pm_only"`
}

type Feed struct {
	Token string `json:"token"`
}

type Digest struct {
	Time             string `json:"time"`
	TelegramBotToken string `json:"telegram_bot_token"`
	TelegramChatID   string `json:"telegram_chat_id"`
	TelegramAPIBase  string `json:"telegram_api_base"`
}

type Auth struct {
	// Users for HTTP basic auth. With no users, everyone is an admin: run it
	// behind Cloudflare Access, Tailscale or a VPN in that case.
	Users []User `json:"users"`
}

type User struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

func Parse(b []byte) (*Config, error) {
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := c.finish(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) finish() error {
	if c.Timezone == "" {
		c.Timezone = "Asia/Bangkok"
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return fmt.Errorf("config: timezone %q: %w", c.Timezone, err)
	}
	c.loc = loc
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.DataFile == "" {
		c.DataFile = "pmtracker-data.json"
	}
	if c.SyncIntervalMinutes <= 0 {
		c.SyncIntervalMinutes = 15
	}
	if c.SyncLookbackMonths <= 0 {
		c.SyncLookbackMonths = 24
	}
	if c.Defaults.IntervalMonths <= 0 {
		c.Defaults.IntervalMonths = 3
	}
	if c.Defaults.WindowDays <= 0 {
		c.Defaults.WindowDays = 21
	}
	if c.Defaults.LeadDays <= 0 {
		c.Defaults.LeadDays = 30
	}
	if c.Defaults.GraceDays <= 0 {
		c.Defaults.GraceDays = 7
	}
	if c.Digest.TelegramAPIBase == "" {
		c.Digest.TelegramAPIBase = "https://api.telegram.org"
	}

	var errs []string
	c.holidays = map[string]bool{}
	for _, h := range c.Holidays {
		if _, err := time.Parse("2006-01-02", h); err != nil {
			errs = append(errs, fmt.Sprintf("holiday %q: want YYYY-MM-DD", h))
		}
		c.holidays[h] = true
	}
	seen := map[string]bool{}
	for i, u := range c.Auth.Users {
		if u.Username == "" || u.Password == "" {
			errs = append(errs, "every user needs a username and a password")
		}
		if seen[u.Username] {
			errs = append(errs, fmt.Sprintf("duplicate user %q", u.Username))
		}
		seen[u.Username] = true
		if u.Role == "" {
			c.Auth.Users[i].Role = RoleViewer
		} else if RoleRank(u.Role) < 0 {
			errs = append(errs, fmt.Sprintf("user %q: role must be viewer, editor or admin", u.Username))
		}
	}
	for _, sc := range c.SeedCalendars {
		if sc.ID == "" {
			errs = append(errs, "every calendar needs an id")
		}
	}
	if len(errs) > 0 {
		return errors.New("config: " + strings.Join(errs, "; "))
	}
	return nil
}

// RoleRank orders roles; -1 for unknown.
func RoleRank(role string) int {
	switch role {
	case RoleViewer:
		return 0
	case RoleEditor:
		return 1
	case RoleAdmin:
		return 2
	}
	return -1
}

func (c *Config) Location() *time.Location { return c.loc }

func (c *Config) IsHoliday(d time.Time) bool { return c.holidays[d.Format("2006-01-02")] }
