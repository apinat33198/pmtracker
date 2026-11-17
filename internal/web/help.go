package web

import (
	"bytes"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/apinat33198/pmtracker/internal/config"
)

// The manual at /help: one page per topic, in Thai, for people using the
// tracker, admins and developers. Each page is templates/help/<slug>.html
// defining "helpbody"; it can use the app's template functions, so ratings,
// statuses and config values shown in the manual come from the code and
// this server's config and can't drift from what the app does.

// helpPage is one page of the manual. The order of helpPages is the order
// of the sidebar and of the previous and next links.
type helpPage struct {
	Slug, Group, Title, Summary string
}

// Manual sections, in sidebar order.
const (
	helpStart = "เริ่มต้น"
	helpUse   = "ใช้งานประจำวัน"
	helpAdmin = "สำหรับ Admin"
	helpDev   = "สำหรับ Developer"
)

var helpGroups = []string{helpStart, helpUse, helpAdmin, helpDev}

var helpPages = []helpPage{
	{"start", helpStart, "PM Tracker ทำอะไร", "แนวคิดหลัก: site, system, vendor, PM task, visit; ข้อมูลไหลจากปฏิทินอย่างไร; บทบาทผู้ใช้; เมนูแต่ละเมนู"},
	{"statuses", helpStart, "สถานะและการคำนวณรอบ PM", "Overdue, Due soon, Booked, Off-cycle, On track, No history; วันครบกำหนดคิดอย่างไร; สถานะของ visit"},
	{"overview", helpUse, "หน้า Overview", "ตาราง site × system, ตัวกรอง, รายการที่เลยกำหนดและ 30 วันข้างหน้า, สรุปตาม vendor"},
	{"sites-tasks", helpUse, "หน้า Site และหน้า Task", "ดูประวัติ PM ของแต่ละ site และแต่ละระบบ, ช่วงจองที่เหมาะ, timeline, บันทึก visit เอง"},
	{"visits", helpUse, "ยืนยันและบันทึก visit", "Confirm done, Mark missed, Undo, บันทึก visit ที่ไม่อยู่ในปฏิทิน, การเลื่อนนัดและการยกเลิก"},
	{"inbox", helpUse, "Review inbox และ Events", "event ที่ระบบจับคู่เองไม่ได้, Link visit, บันทึกเป็นกฎ, Not a PM visit, ดูเหตุผลการจับคู่"},
	{"jobs", helpUse, "Ad-hoc jobs", "งานครั้งเดียวที่ site นอกรอบ PM เช่นงานซ่อม: เพิ่ม, แก้, ปิด, เปิดใหม่"},
	{"findings", helpUse, "Findings (Kaizen)", "สิ่งที่ PM หรือการเดินตรวจพบ: ระดับความรุนแรง, ติดตามจนปิด, เกิดซ้ำ, yokoten, ตัววัดผล"},
	{"incidents", helpUse, "Incidents (ISO 31000)", "เหตุขัดข้องและ near miss: ประเมินความเสี่ยง 5 × 5, การตอบสนอง, การปิด, timeline, risk matrix"},
	{"notifications", helpUse, "Digest, ปฏิทิน และ CSV", "สรุปรายวันทาง Telegram, ฟีดปฏิทิน .ics, การ export CSV, Sync now"},
	{"account", helpUse, "บัญชีและการ sign in", "เข้าสู่ระบบ, Keep me signed in, เปลี่ยนรหัสผ่าน, sign out เครื่องอื่น, การถูกล็อก"},
	{"admin-sites", helpAdmin, "Sites และ PM tasks", "เพิ่ม site พร้อม task, แก้ชื่อ, archive หรือ delete, ตั้งรอบ PM, fixed slots, pause, ตั้งวัน PM ล่าสุด"},
	{"admin-vendors", helpAdmin, "Vendors และ Systems", "ข้อมูลติดต่อ, email domain สำหรับจับคู่, ย้าย task ไป vendor อื่น, ชื่อระบบและชื่ออื่น"},
	{"admin-calendars", helpAdmin, "Calendars และการ sync", "ต่อ Google Calendar ด้วย service account, เพิ่มปฏิทิน, กฎที่จำไว้, ลำดับการจับคู่ event, ฟีดและ digest"},
	{"admin-import", helpAdmin, "Import และ export", "ตั้ง 21 site จาก spreadsheet, คอลัมน์ CSV, รูปแบบวันที่, Check without saving"},
	{"admin-users", helpAdmin, "Users และ Activity log", "เพิ่มผู้ใช้, บทบาท, ตั้งรหัสผ่านใหม่, บัญชีสำรองใน config, บันทึกกิจกรรม 90 วัน"},
	{"admin-backups", helpAdmin, "Backups และการ restore", "สำรองรายวัน, Back up now, ดาวน์โหลด, restore จากรายการหรือจากไฟล์, ข้อควรระวัง"},
	{"dev-architecture", helpDev, "สถาปัตยกรรม", "package ต่าง ๆ, เส้นทางของ request, การ sync, งานเบื้องหลัง, หลักการออกแบบ"},
	{"dev-functions", helpDev, "ฟังก์ชันหลักแต่ละ package", "ฟังก์ชันและ method สำคัญใน core, store, gcal, web, auth, config, backup, notify พร้อมหน้าที่"},
	{"dev-data", helpDev, "ข้อมูลและการจัดเก็บ", "โครงสร้าง data.json, เลขที่และเวอร์ชัน, การเขียนแบบ atomic, activity log, backup, ผลวัดสำหรับ production"},
	{"dev-config", helpDev, "config.json และคำสั่ง", "ทุกตัวเลือกใน config, ค่าเริ่มต้น, คำสั่งและ flag ของ pmtracker"},
	{"dev-deploy", helpDev, "Deploy และดูแลระบบ", "Proxmox LXC, install.sh, อัปเดต, cloudflared, Docker, log, แก้ปัญหาที่พบบ่อย, ลอง branch"},
	{"dev-contributing", helpDev, "เพิ่มฟีเจอร์และเทสต์", "go test, โหมด demo, แนวทางเขียนโค้ดและข้อความ, เช็กลิสต์เมื่อเพิ่มข้อมูลชนิดใหม่, ดูแลคู่มือนี้"},
}

func helpPageBySlug(slug string) (helpPage, int, bool) {
	for i, p := range helpPages {
		if p.Slug == slug {
			return p, i, true
		}
	}
	return helpPage{}, -1, false
}

// helpCtx is what a manual page's template gets.
type helpCtx struct {
	Cfg      *config.Config
	IsEditor bool
	IsAdmin  bool
	Page     helpPage
}

// helpHeading is an h2 on a manual page, for the "on this page" list.
type helpHeading struct{ ID, Text string }

type helpData struct {
	Groups  []helpGroup
	Page    helpPage // zero on the manual's front page and search
	Body    template.HTML
	TOC     []helpHeading
	Prev    helpPage
	Next    helpPage
	Query   string
	Results []helpResult
	Search  bool
}

type helpGroup struct {
	Name  string
	Pages []helpPage
}

type helpResult struct {
	Page    helpPage
	Snippet string
}

func helpGrouped() []helpGroup {
	var out []helpGroup
	for _, g := range helpGroups {
		hg := helpGroup{Name: g}
		for _, p := range helpPages {
			if p.Group == g {
				hg.Pages = append(hg.Pages, p)
			}
		}
		out = append(out, hg)
	}
	return out
}

// parseHelp reads the manual's pages into s.help, one template set each.
func (s *Server) parseHelp(base *template.Template) error {
	s.help = map[string]*template.Template{}
	files, _ := fs.Glob(templateFS, "templates/help/*.html")
	for _, f := range files {
		slug := strings.TrimSuffix(strings.TrimPrefix(f, "templates/help/"), ".html")
		if _, _, ok := helpPageBySlug(slug); !ok {
			return fmt.Errorf("%s isn't listed in helpPages", f)
		}
		t, err := base.Clone()
		if err != nil {
			return err
		}
		if _, err := t.ParseFS(templateFS, f); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		s.help[slug] = t
	}
	for _, p := range helpPages {
		if s.help[p.Slug] == nil {
			return fmt.Errorf("manual page %q has no templates/help/%s.html", p.Slug, p.Slug)
		}
	}
	return nil
}

func (s *Server) helpBody(slug string, ctx helpCtx) (template.HTML, error) {
	var b bytes.Buffer
	if err := s.help[slug].ExecuteTemplate(&b, "helpbody", ctx); err != nil {
		return "", err
	}
	return template.HTML(b.String()), nil
}

var (
	h2Pattern   = regexp.MustCompile(`<h2 id="([^"]+)">(.*?)</h2>`)
	rolePattern = regexp.MustCompile(`\s*<span class="role">[^<]*</span>`) // role tags stay out of "on this page"
)

func (s *Server) helpIndex(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "help", "help", "คู่มือ", s.snapshot(), helpData{Groups: helpGrouped()})
}

func (s *Server) helpPageHandler(w http.ResponseWriter, r *http.Request) {
	p, i, ok := helpPageBySlug(r.PathValue("slug"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	u := userOf(r)
	body, err := s.helpBody(p.Slug, helpCtx{Cfg: s.Cfg, IsEditor: u.can(config.RoleEditor), IsAdmin: u.can(config.RoleAdmin), Page: p})
	if err != nil {
		s.fail(w, err)
		return
	}
	d := helpData{Groups: helpGrouped(), Page: p, Body: body}
	for _, m := range h2Pattern.FindAllStringSubmatch(string(body), -1) {
		text := tagPattern.ReplaceAllString(rolePattern.ReplaceAllString(m[2], ""), "")
		d.TOC = append(d.TOC, helpHeading{ID: m[1], Text: html.UnescapeString(text)})
	}
	if i > 0 {
		d.Prev = helpPages[i-1]
	}
	if i+1 < len(helpPages) {
		d.Next = helpPages[i+1]
	}
	s.render(w, r, "help", "help", p.Title+" | คู่มือ", s.snapshot(), d)
}

var (
	tagPattern   = regexp.MustCompile(`<[^>]*>`)
	blockPattern = regexp.MustCompile(`</?(p|li|td|th|h[1-6]|tr|dt|dd|pre|figcaption|br|div|ul|ol|table|thead|tbody|figure|dl)\b[^>]*>`)
	spacePattern = regexp.MustCompile(`\s+`)
)

// helpTexts is the manual's text, for search: each page rendered once, tags removed.
func (s *Server) helpTexts() map[string]string {
	s.helpOnce.Do(func() {
		s.helpText = map[string]string{}
		for _, p := range helpPages {
			body, err := s.helpBody(p.Slug, helpCtx{Cfg: s.Cfg, IsEditor: true, IsAdmin: true, Page: p})
			if err != nil {
				continue
			}
			t := tagPattern.ReplaceAllString(blockPattern.ReplaceAllString(string(body), " "), "")
			t = html.UnescapeString(t)
			s.helpText[p.Slug] = strings.TrimSpace(spacePattern.ReplaceAllString(t, " "))
		}
	})
	return s.helpText
}

// indexFold is the byte offset in s of the first match of sub, ignoring
// case, or -1.
func indexFold(s, sub string) int {
	n := utf8.RuneCountInString(sub)
	for i := range s {
		end, k := i, 0
		for end < len(s) && k < n {
			_, size := utf8.DecodeRuneInString(s[end:])
			end += size
			k++
		}
		if k == n && strings.EqualFold(s[i:end], sub) {
			return i
		}
	}
	return -1
}

// helpSearch finds pages whose title, summary or text contain the query,
// case-insensitively. Thai has no spaces between words, so it matches text
// rather than whole words.
func (s *Server) helpSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(q) > 100 {
		q = string([]rune(q)[:100])
	}
	d := helpData{Groups: helpGrouped(), Query: q, Search: true}
	if q != "" {
		texts := s.helpTexts()
		var titled, others []helpResult
		for _, p := range helpPages {
			text := texts[p.Slug]
			inTitle := indexFold(p.Title+" "+p.Summary, q) >= 0
			at := indexFold(text, q)
			if !inTitle && at < 0 {
				continue
			}
			res := helpResult{Page: p, Snippet: p.Summary}
			if at >= 0 {
				res.Snippet = snippet(text, at, len(q))
			}
			if inTitle {
				titled = append(titled, res)
			} else {
				others = append(others, res)
			}
		}
		d.Results = append(titled, others...)
	}
	s.render(w, r, "help", "help", "ค้นหาในคู่มือ", s.snapshot(), d)
}

// snippet is about 160 characters of text around a match at byte offset at.
func snippet(text string, at, n int) string {
	start, end := at, at+n
	for i := 0; i < 70 && start > 0; i++ {
		_, size := utf8.DecodeLastRuneInString(text[:start])
		start -= size
	}
	for i := 0; i < 90 && end < len(text); i++ {
		_, size := utf8.DecodeRuneInString(text[end:])
		end += size
	}
	// Don't cut through a Thai syllable: a vowel or tone mark belongs with
	// the letter before it.
	for start > 0 {
		r, _ := utf8.DecodeRuneInString(text[start:])
		if !unicode.Is(unicode.Mn, r) {
			break
		}
		_, size := utf8.DecodeLastRuneInString(text[:start])
		start -= size
	}
	for end < len(text) {
		r, size := utf8.DecodeRuneInString(text[end:])
		if !unicode.Is(unicode.Mn, r) {
			break
		}
		end += size
	}
	out := text[start:end]
	if start > 0 {
		out = "…" + out
	}
	if end < len(text) {
		out += "…"
	}
	return out
}
