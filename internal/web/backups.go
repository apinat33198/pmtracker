package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/apinat33198/pmtracker/internal/backup"
)

type backupsData struct {
	Files    []backup.File
	Dir      string
	Time     string
	KeepDays int
	LastErr  string
}

func (s *Server) adminBackups(w http.ResponseWriter, r *http.Request) {
	d := backupsData{Dir: s.Backups.Dir, Time: s.Cfg.Backup.Time, KeepDays: s.Cfg.Backup.KeepDays}
	files, err := s.Backups.List()
	if err != nil {
		d.LastErr = err.Error()
	}
	d.Files = files
	if _, lastErr := s.Backups.Status(); lastErr != nil {
		d.LastErr = lastErr.Error()
	}
	s.render(w, r, "admin_backups", "admin", "Backups", s.snapshot(), d)
}

func (s *Server) adminBackupNow(w http.ResponseWriter, r *http.Request) {
	f, err := s.Backups.Create(backup.Manual)
	if err != nil {
		s.redirect(w, r, "/admin/backups", "The backup failed: "+err.Error(), "error")
		return
	}
	s.Store.Log(userOf(r).Name, "Made a backup by hand: "+f.Name)
	s.redirect(w, r, "/admin/backups", fmt.Sprintf("Saved %s (%s).", f.Name, sizeText(f.Size)), "")
}

func (s *Server) adminBackupFile(w http.ResponseWriter, r *http.Request) {
	fh, f, err := s.Backups.Open(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer fh.Close()
	// A backup holds password hashes: note who took one.
	s.Store.Log(userOf(r).Name, "Downloaded the backup "+f.Name)
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+f.Name+`"`)
	http.ServeContent(w, r, f.Name, f.At, fh)
}

func (s *Server) adminRestoreBackup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	if !confirmedRestore(r) {
		s.redirect(w, r, "/admin/backups", "Type restore in the box to confirm. Nothing was changed.", "error")
		return
	}
	data, err := s.Backups.Read(name)
	if err != nil {
		s.redirect(w, r, "/admin/backups", "That backup can't be read: "+err.Error(), "error")
		return
	}
	s.restore(w, r, data, name)
}

func (s *Server) adminRestoreUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		msg := "The upload could not be read: " + err.Error()
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			msg = "That file is larger than any backup should be."
		}
		s.redirect(w, r, "/admin/backups", msg, "error")
		return
	}
	if !confirmedRestore(r) {
		s.redirect(w, r, "/admin/backups", "Type restore in the box to confirm. Nothing was changed.", "error")
		return
	}
	fh, hdr, err := r.FormFile("file")
	if err != nil {
		s.redirect(w, r, "/admin/backups", "Choose a backup file to restore.", "error")
		return
	}
	defer fh.Close()
	data, err := backup.Decode(fh)
	if err != nil {
		s.redirect(w, r, "/admin/backups", err.Error(), "error")
		return
	}
	s.restore(w, r, data, "the uploaded file "+cleanName(hdr.Filename))
}

// cleanName keeps an uploaded file's name safe to show and log.
func cleanName(name string) string {
	var b strings.Builder
	for _, c := range name {
		if b.Len() >= 80 {
			b.WriteString("…")
			break
		}
		if c < 0x20 || c == 0x7f {
			continue
		}
		b.WriteRune(c)
	}
	if b.Len() == 0 {
		return "(no name)"
	}
	return b.String()
}

func confirmedRestore(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.FormValue("confirm")), "restore")
}

// restore saves a copy of the current data, then replaces it with data.
func (s *Server) restore(w http.ResponseWriter, r *http.Request, data []byte, from string) {
	u := userOf(r)
	pre, err := s.Backups.Create(backup.BeforeRestore)
	if err != nil {
		s.redirect(w, r, "/admin/backups", "Couldn't save a copy of the current data first, so nothing was restored: "+err.Error(), "error")
		return
	}
	if err := s.Store.Restore(data, u.Name, "Restored all data from "+from); err != nil {
		s.redirect(w, r, "/admin/backups", "Nothing was restored: "+err.Error(), "error")
		return
	}
	s.Log.Printf("restore: %s restored all data from %q (previous data saved as %s)", u.Name, from, pre.Name)
	// Everyone, including whoever restored, signs in again.
	s.clearSession(w, r)
	s.redirect(w, r, "/login?next=%2Fadmin%2Fbackups", fmt.Sprintf("Restored %s. Everyone has been signed out, so sign in again. The data from just before is saved as %s, in case you need it back.", from, pre.Name), "")
}

func sizeText(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", (n+1023)/1024)
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

func backupKind(kind string) string {
	switch kind {
	case backup.Manual:
		return "Made by hand"
	case backup.BeforeRestore:
		return "Saved before a restore"
	}
	return "Daily copy"
}

// backupWhen shows the date, plus the time for copies that aren't daily.
func backupWhen(f backup.File) string {
	if f.Kind == backup.Daily {
		return f.At.Format("Mon 2 Jan 2006")
	}
	return f.At.Format("Mon 2 Jan 2006, 15:04")
}
