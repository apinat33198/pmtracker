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
	"net/netip"
	"net/url"
	"os"
	"regexp"
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
	// TrustedProxies are reverse proxies (addresses or CIDRs) whose
	// X-Forwarded-For header is believed. Default: loopback only. Behind
	// Docker or a proxy on another host, add its address, e.g. "172.16.0.0/12".
	TrustedProxies []string `json:"trusted_proxies"`

	loc      *time.Location
	holidays map[string]bool
	proxies  []netip.Prefix
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
	// AnonymousAccess is what visitors who haven't signed in may do:
	// "none" (the default; sign-in required for every page) or "viewer"
	// (read-only pages open, sign-in needed to make changes).
	AnonymousAccess string `json:"anonymous_access"`
	// SessionHours is how long a sign-in lasts without "keep me signed in" (default 12).
	SessionHours int `json:"session_hours"`
	// Users defined here are optional: accounts are normally managed under
	// Admin, Users. A config user works as a break-glass account that can't
	// be changed from the web. Password may be a hash from
	// "pmtracker hash-password" (recommended) or plain text.
	Users []User `json:"users"`
}

type User struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// Anonymous access levels.
const (
	AnonymousNone   = "none"
	AnonymousViewer = "viewer"
)

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
	switch c.Auth.AnonymousAccess {
	case "":
		c.Auth.AnonymousAccess = AnonymousNone
	case AnonymousNone, AnonymousViewer:
	default:
		errs = append(errs, `auth.anonymous_access must be "none" or "viewer"`)
	}
	if c.Auth.SessionHours <= 0 {
		c.Auth.SessionHours = 12
	}
	seen := map[string]bool{}
	for i, u := range c.Auth.Users {
		if u.Username == "" || u.Password == "" {
			errs = append(errs, "every user needs a username and a password")
		} else if !usernameRe.MatchString(u.Username) {
			errs = append(errs, fmt.Sprintf("user %q: usernames are 2-32 lowercase letters, digits, dot, dash or underscore", u.Username))
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
	if c.PublicURL != "" {
		u, err := url.Parse(c.PublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Sprintf("public_url %q: want https://host[:port]", c.PublicURL))
		}
	}
	if len(c.TrustedProxies) == 0 {
		c.TrustedProxies = []string{"127.0.0.0/8", "::1/128"}
	}
	c.proxies = nil
	for _, p := range c.TrustedProxies {
		pfx, err := netip.ParsePrefix(strings.TrimSpace(p))
		if err != nil {
			a, err2 := netip.ParseAddr(strings.TrimSpace(p))
			if err2 != nil {
				errs = append(errs, fmt.Sprintf("trusted_proxies: %q is not an address or CIDR", p))
				continue
			}
			pfx = netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())
		}
		c.proxies = append(c.proxies, pfx.Masked())
	}
	if len(errs) > 0 {
		return errors.New("config: " + strings.Join(errs, "; "))
	}
	return nil
}

var usernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,31}$`)

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

// TrustedProxy reports whether a is one of the configured reverse proxies.
func (c *Config) TrustedProxy(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range c.proxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// HTTPS reports whether the tracker is served over HTTPS (public_url).
func (c *Config) HTTPS() bool { return strings.HasPrefix(strings.ToLower(c.PublicURL), "https://") }

// placeholderToken is the feed token in config.example.json.
const placeholderToken = "replace-with-a-long-random-string"

// SecretProblems lists settings that aren't safe to go live with.
func (c *Config) SecretProblems() []string {
	var out []string
	if t := c.Feed.Token; t != "" && (t == placeholderToken || len(t) < 24) {
		out = append(out, "feed.token is the example value or shorter than 24 characters: set a random one (openssl rand -hex 24) or leave it empty to turn the calendar feed off")
	}
	return out
}

func (c *Config) IsHoliday(d time.Time) bool { return c.holidays[d.Format("2006-01-02")] }
