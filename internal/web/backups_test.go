package web

import (
	"bytes"
	"compress/gzip"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/apinat33198/pmtracker/internal/backup"
	"github.com/apinat33198/pmtracker/internal/config"
)

func TestBackupsPage(t *testing.T) {
	e := demoServer(t, []config.User{{Username: "admin", Password: "pw", Role: config.RoleAdmin},
		{Username: "ed", Password: "pw", Role: config.RoleEditor}})
	if body := get(t, e, "/admin/backups"); !strings.Contains(body, "No backups yet") || !strings.Contains(body, "every day at 02:30") {
		t.Errorf("empty page: %.300s", body[strings.Index(body, "<main>"):])
	}
	if msg := post(t, e, "/admin/backups", url.Values{}); !strings.Contains(msg, "-manual.json.gz") {
		t.Fatalf("back up now: %q", msg)
	}
	files, _ := e.server.Backups.List()
	if len(files) != 1 {
		t.Fatalf("files = %+v", files)
	}
	name := files[0].Name
	if body := get(t, e, "/admin/backups"); !strings.Contains(body, "Made by hand") || !strings.Contains(body, "/admin/backups/file/"+name) {
		t.Error("the page should list the new backup")
	}

	// Download: admins only, a real gzip of the data, and it's logged.
	code, body, _ := req(t, "GET", e.srv.URL+"/admin/backups/file/"+name, "admin", nil)
	if code != 200 {
		t.Fatalf("download = %d", code)
	}
	zr, err := gzip.NewReader(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(zr)
	if !strings.Contains(string(data), `"BKK1"`) || strings.Contains(string(data), "session_secret") {
		t.Error("the download should be the data, without the session key")
	}
	if a := e.store.Audit(1); !strings.Contains(a[0].Action, "Downloaded the backup") {
		t.Errorf("download not logged: %+v", a[0])
	}
	if code, _, _ := req(t, "GET", e.srv.URL+"/admin/backups/file/"+name, "ed", nil); code != http.StatusForbidden {
		t.Errorf("editor download = %d", code)
	}
	for _, bad := range []string{"..%2fd.json", "pmtracker-2026-11-03.json.gz", "x"} {
		if code, _, _ := req(t, "GET", e.srv.URL+"/admin/backups/file/"+bad, "admin", nil); code != http.StatusNotFound {
			t.Errorf("download %q = %d", bad, code)
		}
	}

	// Restore needs the word typed; then it brings the old data back.
	post(t, e, "/admin/sites", url.Values{"code": {"PTT1"}, "name": {"Pattaya"}})
	if _, ok := e.store.Catalog().SiteByCode("PTT1"); !ok {
		t.Fatal("setup")
	}
	if msg := post(t, e, "/admin/backups/restore", url.Values{"name": {name}, "confirm": {"yes"}}); !strings.Contains(msg, "Type restore") {
		t.Errorf("unconfirmed restore: %q", msg)
	}
	if _, ok := e.store.Catalog().SiteByCode("PTT1"); !ok {
		t.Fatal("an unconfirmed restore changed the data")
	}
	msg := post(t, e, "/admin/backups/restore", url.Values{"name": {name}, "confirm": {"Restore"}})
	if !strings.Contains(msg, "Restored "+name) || !strings.Contains(msg, "before-restore.json.gz") {
		t.Fatalf("restore: %q", msg)
	}
	if _, ok := e.store.Catalog().SiteByCode("PTT1"); ok {
		t.Error("the site added after the backup should be gone")
	}
	files, _ = e.server.Backups.List()
	var pre string
	for _, f := range files {
		if f.Kind == backup.BeforeRestore {
			pre = f.Name
		}
	}
	if pre == "" {
		t.Fatal("a copy of the data from before the restore should be saved")
	}

	// Upload: the pre-restore copy brings PTT1 back.
	preData, _ := e.server.Backups.Read(pre)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "data.json")
	fw.Write(preData)
	mw.WriteField("confirm", "restore")
	mw.Close()
	r, _ := http.NewRequest("POST", e.srv.URL+"/admin/backups/upload", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.SetBasicAuth("admin", "pw")
	resp, err := noFollow.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if lu, _ := url.Parse(resp.Header.Get("Location")); !strings.Contains(lu.Query().Get("msg"), "Restored the uploaded file data.json") {
		t.Errorf("upload restore: %q", lu.Query().Get("msg"))
	}
	if _, ok := e.store.Catalog().SiteByCode("PTT1"); !ok {
		t.Error("the uploaded backup should bring PTT1 back")
	}

	// Something that isn't a backup is refused and changes nothing.
	buf.Reset()
	mw = multipart.NewWriter(&buf)
	fw, _ = mw.CreateFormFile("file", "notes.json")
	fw.Write([]byte(`{"hello": "world"}`))
	mw.WriteField("confirm", "restore")
	mw.Close()
	r, _ = http.NewRequest("POST", e.srv.URL+"/admin/backups/upload", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.SetBasicAuth("admin", "pw")
	resp, _ = noFollow.Do(r)
	resp.Body.Close()
	if lu, _ := url.Parse(resp.Header.Get("Location")); !strings.Contains(lu.Query().Get("msg"), "isn't a PM tracker backup") {
		t.Errorf("bad upload: %q", lu.Query().Get("msg"))
	}
	if _, ok := e.store.Catalog().SiteByCode("PTT1"); !ok {
		t.Error("a refused upload must leave the data alone")
	}
}
