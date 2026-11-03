// Command pmtracker tracks vendor preventive-maintenance visits from Google
// Calendar and forecasts each asset's next PM.
//
//	pmtracker -demo                       run with built-in demo data
//	pmtracker -config config.json         serve (default command)
//	pmtracker -config config.json check   test the service account and calendars
//	pmtracker -config config.json sync    sync once and exit
//	pmtracker -config config.json digest  print today's digest
//	pmtracker hash-password               hash a password for the config file
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // the binary works on hosts without zoneinfo

	"github.com/apinat33198/pmtracker/internal/auth"
	"github.com/apinat33198/pmtracker/internal/backup"
	"github.com/apinat33198/pmtracker/internal/config"
	"github.com/apinat33198/pmtracker/internal/core"
	"github.com/apinat33198/pmtracker/internal/demo"
	"github.com/apinat33198/pmtracker/internal/gcal"
	"github.com/apinat33198/pmtracker/internal/notify"
	"github.com/apinat33198/pmtracker/internal/store"
	"github.com/apinat33198/pmtracker/internal/web"
)

func main() {
	cfgPath := flag.String("config", "", "path to config.json")
	demoMode := flag.Bool("demo", false, "run with built-in demo data; no Google account needed")
	keep := flag.Bool("keep", false, "demo: keep the existing demo data file instead of starting fresh")
	addr := flag.String("addr", "", "listen address, overrides the config")
	dataPath := flag.String("data", "", "data file, overrides the config")
	todayFlag := flag.String("today", "", "pretend today is YYYY-MM-DD (for demos and testing)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: pmtracker [flags] [serve|check|sync|digest|hash-password]\n\nFlags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	cmd := flag.Arg(0)
	if cmd == "" {
		cmd = "serve"
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)
	if cmd == "hash-password" {
		if err := hashPassword(); err != nil {
			logger.Fatal(err)
		}
		return
	}
	if err := run(cmd, *cfgPath, *demoMode, *keep, *addr, *dataPath, *todayFlag, logger); err != nil {
		logger.Fatal(err)
	}
}

func run(cmd, cfgPath string, demoMode, keep bool, addr, dataPath, todayFlag string, logger *log.Logger) error {
	now := time.Now
	if todayFlag != "" {
		d, err := time.Parse("2006-01-02", todayFlag)
		if err != nil {
			return fmt.Errorf("-today: %w", err)
		}
		real := time.Now()
		offset := time.Date(d.Year(), d.Month(), d.Day(), real.Hour(), real.Minute(), real.Second(), 0, real.Location()).Sub(real)
		now = func() time.Time { return time.Now().Add(offset) }
	}

	var (
		cfg     *config.Config
		src     gcal.Source
		saMail  string
		demoCat core.Catalog
		err     error
	)
	switch {
	case demoMode:
		if cfg, err = demo.Config(); err != nil {
			return err
		}
		demoCat = demo.Catalog(now())
		src = demo.NewSource(cfg, &demoCat, now())
	case cfgPath == "":
		return errors.New("pass -config config.json, or -demo to try it with built-in data")
	default:
		if cfg, err = config.Load(cfgPath); err != nil {
			return err
		}
		// Without a service-account key the tracker still runs: sites, tasks,
		// users and visits typed in by hand work, and calendars sync once the
		// key is in place.
		keyFile := cfg.Google.CredentialsFile
		if _, statErr := os.Stat(keyFile); keyFile == "" || errors.Is(statErr, os.ErrNotExist) {
			d := gcal.Disconnected{KeyFile: keyFile}
			logger.Print(d.Err())
			src = d
		} else {
			g, err := gcal.NewGoogle(keyFile, cfg.Location())
			if err != nil {
				return err
			}
			src, saMail = g, g.ClientEmail()
		}
	}
	if addr != "" {
		cfg.Listen = addr
	}
	if dataPath != "" {
		cfg.DataFile = dataPath
	}

	if demoMode && !keep {
		// Only ever replace demo data: "-demo -data <real file>" must not wipe it.
		if old, err := store.Open(cfg.DataFile); err == nil {
			if cat := old.Catalog(); !old.IsDemo() && len(cat.Sites)+len(old.Users()) > 0 && filepath.Base(cfg.DataFile) != "pmtracker-demo.json" {
				return fmt.Errorf("%s isn't demo data; refusing to replace it. Pick another -data file for the demo", cfg.DataFile)
			}
		}
		if err := os.Remove(cfg.DataFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		os.RemoveAll(store.ActivityDir(cfg.DataFile))
	}
	if !demoMode {
		if problems := cfg.SecretProblems(); len(problems) > 0 {
			return errors.New("config: " + strings.Join(problems, "; "))
		}
	}
	st, err := store.Open(cfg.DataFile)
	if err != nil {
		return err
	}
	st.SetClock(now)
	st.Logf = logger.Printf
	if err := st.Activity().SetKeepDays(cfg.Activity.KeepDays); err != nil {
		logger.Printf("activity log: %v", err)
	}
	// A second copy of every activity entry in the server log (journald),
	// outside the tracker's own files.
	st.Activity().OnAppend = func(e store.AuditEntry) {
		from := ""
		if e.From != "" {
			from = " from " + e.From
		}
		logger.Printf("activity: %s%s: %s", e.By, from, e.Action)
	}
	if cat := st.Catalog(); len(cat.Sites) == 0 && len(cat.Systems) == 0 && len(cat.Calendars) == 0 {
		// First run: seed the catalog.
		err := st.UpdateCatalog("system", "Created the data file", func(c *core.Catalog) error {
			if demoMode {
				*c = demoCat
				return nil
			}
			c.Systems = core.DefaultSystems()
			for _, sc := range cfg.SeedCalendars {
				c.Calendars = append(c.Calendars, core.Calendar{ID: sc.ID, Label: sc.Label, PMOnly: sc.PMOnly})
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if demoMode && !st.IsDemo() {
		if err := st.MarkDemo(); err != nil {
			return err
		}
	}
	if demoMode && len(st.Users()) == 0 {
		users, err := demo.Users(now())
		if err != nil {
			return err
		}
		for _, u := range users {
			if err := st.SaveUser(u, "system", ""); err != nil {
				return err
			}
		}
	}
	syncer := &gcal.Syncer{Store: st, Source: src, Cfg: cfg, Now: now, Log: logger}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "check":
		return check(ctx, cfg, st, src, saMail, now())
	case "sync":
		failed := false
		for _, r := range syncer.SyncAll(ctx) {
			if r.Err != nil {
				failed = true
				fmt.Printf("%s: %v\n", r.Calendar, r.Err)
				continue
			}
			fmt.Printf("%s: ok (full=%v, %d changes)\n", r.Calendar, r.Full, r.Changes)
		}
		if failed {
			return errors.New("sync failed")
		}
		return nil
	case "digest":
		if demoMode {
			demoSeed(ctx, syncer, st, cfg, now)
		}
		d := core.Digest(core.Build(cfg, st.Inputs(), now()), cfg.PublicURL)
		if d == "" {
			d = "Nothing needs attention today."
		}
		fmt.Println(d)
		return nil
	case "serve":
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}

	if demoMode && !keep {
		demoSeed(ctx, syncer, st, cfg, now)
	}
	go syncer.Run(ctx, time.Duration(cfg.SyncIntervalMinutes)*time.Minute)

	if cfg.Digest.Time != "" {
		tg := notify.Telegram{APIBase: cfg.Digest.TelegramAPIBase, BotToken: cfg.Digest.TelegramBotToken, ChatID: cfg.Digest.TelegramChatID}
		go func() {
			err := notify.Daily(ctx, cfg.Digest.Time, cfg.Location(), func(ctx context.Context) {
				d := core.Digest(core.Build(cfg, st.Inputs(), now()), cfg.PublicURL)
				if d == "" {
					logger.Print("digest: nothing needs attention")
					return
				}
				if tg.BotToken == "" || tg.ChatID == "" {
					logger.Printf("digest (no Telegram chat configured):\n%s", d)
					return
				}
				if err := tg.Send(ctx, d); err != nil {
					logger.Printf("digest: %v", err)
				}
			})
			if err != nil {
				logger.Print(err)
			}
		}()
	}

	backups := &backup.Manager{Dir: cfg.BackupDir(), KeepDays: cfg.Backup.KeepDays, Store: st, Now: now, Loc: cfg.Location(), Log: logger}
	if demoMode {
		backups.Dir = filepath.Join(os.TempDir(), "pmtracker-demo-backups")
	}
	if cfg.Backup.Time != "off" {
		go backups.Run(ctx, cfg.Backup.Time)
	}

	srv := &web.Server{Cfg: cfg, Store: st, Syncer: syncer, Now: now, Log: logger, Demo: demoMode, ServiceAccount: saMail, Backups: backups}
	h, err := srv.Handler()
	if err != nil {
		return err
	}
	hs := &http.Server{Addr: cfg.Listen, Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute,
		WriteTimeout: 2 * time.Minute, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 64 << 10}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	if w := cfg.ProxyWarning(); w != "" {
		logger.Print("Warning: " + w)
	}
	logger.Printf("PM tracker listening on %s (data: %s, timezone: %s)", cfg.Listen, cfg.DataFile, cfg.Timezone)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// demoSeed runs the initial full sync and the follow-up incremental sync
// (which carries a reschedule and a cancellation), then confirms visits
// older than two weeks so only recent ones wait for confirmation.
func demoSeed(ctx context.Context, sy *gcal.Syncer, st *store.Store, cfg *config.Config, now func() time.Time) {
	sy.SyncAll(ctx)
	sy.SyncAll(ctx)
	snap := core.Build(cfg, st.Inputs(), now())
	cutoff := snap.Today.AddDate(0, 0, -14)
	var marks []core.VisitMark
	for _, v := range snap.Visits {
		if v.Status == core.VisitUnconfirmed && v.Date.Before(cutoff) {
			marks = append(marks, core.VisitMark{VisitID: v.ID, Status: "done", By: "demo", At: v.Date.AddDate(0, 0, 1)})
		}
	}
	st.MarkAll(marks, "")
}

// check verifies credentials and calendar access without touching the store.
func check(ctx context.Context, cfg *config.Config, st *store.Store, src gcal.Source, saMail string, now time.Time) error {
	if saMail != "" {
		fmt.Printf("Service account: %s\n", saMail)
	}
	cat := st.Catalog()
	if len(cat.Calendars) == 0 {
		return errors.New("no calendars yet: add one under Admin, Calendars and sync, or in the config file before the first run")
	}
	m := core.NewMatcher(&cat, nil, cfg.Keywords)
	failed := false
	for _, c := range cat.Calendars {
		evs, _, err := src.List(ctx, c.ID, "", now.AddDate(0, -cfg.SyncLookbackMonths, 0))
		if err != nil {
			failed = true
			fmt.Printf("✗ %s (%s): %v\n", c.Label, c.ID, err)
			continue
		}
		counts := map[core.MatchClass]int{}
		for _, e := range evs {
			counts[m.Match(e, nil).Class]++
		}
		fmt.Printf("✓ %s: %d events; %d PM visits, %d need review, %d not PM\n",
			c.Label, len(evs), counts[core.MatchFull], counts[core.MatchPartial], counts[core.MatchNone])
	}
	if failed {
		return errors.New("check failed")
	}
	return nil
}

// hashPassword reads a password from stdin and prints its hash for the
// password field of a user in the config file.
func hashPassword() error {
	tty := false
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		tty = true
		fmt.Fprint(os.Stderr, "Password: ")
		if err := stty("-echo"); err == nil {
			defer stty("echo")
		}
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return errors.New("no password given")
	}
	if tty {
		fmt.Fprintln(os.Stderr)
	}
	pw := strings.TrimRight(line, "\r\n")
	if pw == "" {
		return errors.New("no password given")
	}
	if msg := auth.CheckPassword(pw, ""); msg != "" {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
	}
	h, err := auth.Hash(pw)
	if err != nil {
		return err
	}
	fmt.Println(h)
	return nil
}

func stty(arg string) error {
	cmd := exec.Command("stty", arg)
	cmd.Stdin = os.Stdin
	return cmd.Run()
}
