package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/apinat33198/pmtracker/internal/config"
)

func TestHelpPagesRender(t *testing.T) {
	e := demoServer(t, []config.User{
		{Username: "admin", Password: "pw", Role: config.RoleAdmin},
		{Username: "v", Password: "pw", Role: config.RoleViewer},
	})
	index := get(t, e, "/help")
	for _, p := range helpPages {
		if !strings.Contains(index, `href="/help/`+p.Slug+`"`) {
			t.Errorf("the manual's front page doesn't link to %s", p.Slug)
		}
		page := get(t, e, "/help/"+p.Slug)
		if !strings.Contains(page, "<h1>"+p.Title+"</h1>") || !strings.Contains(page, `aria-current="page">`+p.Title) {
			t.Errorf("/help/%s lacks its title or isn't marked current in the sidebar", p.Slug)
		}
		// Viewers can read every page too.
		if code, body, _ := req(t, "GET", e.srv.URL+"/help/"+p.Slug, "v", nil); code != 200 || strings.Contains(body, "ZgotmplZ") {
			t.Errorf("viewer GET /help/%s = %d", p.Slug, code)
		}
	}
	if code, _, _ := req(t, "GET", e.srv.URL+"/help/nope", "admin", nil); code != http.StatusNotFound {
		t.Errorf("an unknown manual page = %d", code)
	}
	if code, _, _ := req(t, "GET", e.srv.URL+"/help", "", nil); code != http.StatusSeeOther {
		t.Errorf("the manual without signing in = %d, want a redirect to sign in", code)
	}
	if home := get(t, e, "/"); !strings.Contains(home, `href="/help"`) || !strings.Contains(home, `href="/help/overview"`) {
		t.Error("the account menu and the overview should link to the manual")
	}
	// Prev and next follow helpPages.
	if p := get(t, e, "/help/"+helpPages[1].Slug); !strings.Contains(p, `class="prev" href="/help/`+helpPages[0].Slug+`"`) ||
		!strings.Contains(p, `class="next" href="/help/`+helpPages[2].Slug+`"`) {
		t.Error("previous and next links")
	}
	// Values come from the code and this server's config.
	st := get(t, e, "/help/statuses")
	if !strings.Contains(st, "เกิน "+strconv.Itoa(e.server.Cfg.Defaults.GraceDays)+" วัน") {
		t.Error("the statuses page should show this server's grace days")
	}
	inc := get(t, e, "/help/incidents")
	for _, w := range []string{`risk-extreme">25</td>`, "Insignificant", "Almost certain", "Retain"} {
		if !strings.Contains(inc, w) {
			t.Errorf("the incidents page lacks %q from the code's risk criteria", w)
		}
	}
	if p := get(t, e, "/help/incidents"); !strings.Contains(p, `<a href="#close">`) {
		t.Error("the page should list its sections")
	}
}

func TestHelpSearch(t *testing.T) {
	e := demoServer(t, nil)
	for q, want := range map[string]string{
		"Confirm done": "/help/visits",
		"off-cycle":    "/help/statuses",
		"ยืนยัน":       "/help/visits",
		"Litestream":   "/help/dev-data",
		"yokoten":      "/help/findings",
	} {
		p := get(t, e, "/help/search?q="+url.QueryEscape(q))
		if !strings.Contains(p, `href="/help/`+strings.TrimPrefix(want, "/help/")+`"`) || !strings.Contains(p, "<li><a href=\""+want+"\"") {
			t.Errorf("searching %q should find %s", q, want)
		}
	}
	if p := get(t, e, "/help/search?q="+url.QueryEscape("zzqx-nothing")); !strings.Contains(p, "0 หน้าที่มีคำว่า") || !strings.Contains(p, "ไม่พบ") {
		t.Error("a search with no results should say so")
	}
	get(t, e, "/help/search")
	get(t, e, "/help/search?q="+url.QueryEscape(strings.Repeat("ก", 300)))
}

func TestIndexFoldAndSnippet(t *testing.T) {
	if i := indexFold("Status: Off-cycle", "OFF-CYCLE"); i != 8 {
		t.Errorf("indexFold = %d", i)
	}
	if i := indexFold("ยืนยันว่าทำแล้ว", "ว่า"); i != len("ยืนยัน") {
		t.Errorf("indexFold Thai = %d", i)
	}
	if indexFold("abc", "abcd") != -1 || indexFold("", "a") != -1 {
		t.Error("no match")
	}
	// Thai vowels and tone marks stay with their letter at the cut.
	marks := strings.Repeat("กี้", 100)
	if s := snippet(marks, len(marks)/2, 3); strings.HasPrefix(s, "…ี") || strings.HasPrefix(s, "…้") {
		t.Errorf("snippet starts mid-syllable: %q", s[:12])
	}
	text := strings.Repeat("ก", 200) + "เป้า" + strings.Repeat("ข", 200)
	s := snippet(text, indexFold(text, "เป้า"), len("เป้า"))
	if !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") || !strings.Contains(s, "เป้า") || !utf8Valid(s) {
		t.Errorf("snippet = %q", s)
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "�") == s }

var (
	hrefPattern = regexp.MustCompile(`href="(/[^"]*)"`)
	idPattern   = regexp.MustCompile(`id="([^"]+)"`)
)

// Every link in the manual, and every link from the app into the manual,
// opens; links to a section of a manual page find that section.
func TestHelpLinks(t *testing.T) {
	e := demoServer(t, nil)
	pages := map[string]string{"/help": get(t, e, "/help")}
	for _, p := range helpPages {
		pages["/help/"+p.Slug] = get(t, e, "/help/"+p.Slug)
	}
	// The app's own links into the manual.
	for _, p := range []string{"/", "/inbox", "/events", "/jobs", "/findings", "/incidents", "/admin/sites", "/admin/vendors", "/admin/calendars",
		"/admin/import", "/admin/backups", "/admin/users", "/admin/activity", "/admin/systems"} {
		pages[p] = get(t, e, p)
	}
	checked := map[string]bool{}
	for from, body := range pages {
		isHelp := strings.HasPrefix(from, "/help")
		for _, m := range hrefPattern.FindAllStringSubmatch(body, -1) {
			link := strings.ReplaceAll(m[1], "&amp;", "&")
			if strings.HasPrefix(link, "/static/") || (!isHelp && !strings.HasPrefix(link, "/help")) {
				continue
			}
			path, frag, _ := strings.Cut(link, "#")
			if path == "/help/search" || strings.HasPrefix(path, "/help/search?") {
				continue
			}
			target, ok := pages[path]
			if !ok {
				if checked[path] {
					continue
				}
				checked[path] = true
				code, _, _ := req(t, "GET", e.srv.URL+path, "admin", nil)
				if code != 200 {
					t.Errorf("%s links to %s, which returns %d", from, path, code)
				}
				continue
			}
			if frag == "" || !strings.HasPrefix(path, "/help") {
				continue
			}
			found := false
			for _, id := range idPattern.FindAllStringSubmatch(target, -1) {
				if id[1] == frag {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s links to %s#%s, which has no such section", from, path, frag)
			}
		}
	}
	// Links with anchors into the app (outside the manual) at least land on a page.
	for _, p := range []string{"/admin/calendars#digest", "/admin/calendars#feed", "/admin/calendars#rules"} {
		path, frag, _ := strings.Cut(p, "#")
		if !strings.Contains(get(t, e, path), `id="`+frag+`"`) {
			t.Errorf("%s has no section %s", path, frag)
		}
	}
}

// Every function the developer pages name exists in the code, so renaming
// or removing one fails this test until the manual is updated.
func TestHelpFunctionsExist(t *testing.T) {
	e := demoServer(t, nil)
	fnPattern := regexp.MustCompile(`<code class="fn">([^<]+)</code>`)
	dirs := map[string]string{"main": "../../cmd/pmtracker", "web": "."}
	known := map[string]map[string]bool{}
	load := func(pkg string) map[string]bool {
		if k, ok := known[pkg]; ok {
			return k
		}
		dir, ok := dirs[pkg]
		if !ok {
			dir = filepath.Join("..", pkg)
		}
		names := map[string]bool{}
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", dir, err)
		}
		for _, p := range pkgs {
			for _, f := range p.Files {
				for _, d := range f.Decls {
					fd, ok := d.(*ast.FuncDecl)
					if !ok {
						continue
					}
					name := fd.Name.Name
					if fd.Recv != nil && len(fd.Recv.List) > 0 {
						typ := fd.Recv.List[0].Type
						if s, ok := typ.(*ast.StarExpr); ok {
							typ = s.X
						}
						if id, ok := typ.(*ast.Ident); ok {
							name = id.Name + "." + name
						}
					}
					names[name] = true
				}
			}
		}
		known[pkg] = names
		return names
	}
	n := 0
	for _, p := range helpPages {
		body := get(t, e, "/help/"+p.Slug)
		for _, m := range fnPattern.FindAllStringSubmatch(body, -1) {
			pkg, rest, ok := strings.Cut(m[1], ".")
			if !ok {
				t.Errorf("/help/%s: %q isn't package.Name", p.Slug, m[1])
				continue
			}
			if !load(pkg)[rest] {
				t.Errorf("/help/%s names %s, which isn't in package %s", p.Slug, m[1], pkg)
			}
			n++
		}
	}
	if n < 80 {
		t.Errorf("only %d functions checked; the class=\"fn\" markup may have changed", n)
	}
}
