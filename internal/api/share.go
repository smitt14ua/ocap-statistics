package api

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Chat apps (Discord, Telegram, ...) build link previews from Open Graph tags and run no
// scripts, so a link to a player (?player=) or a mission (?mission=) gets its page with tags
// that describe it. The texts come from the page's own translations (web/static/i18n), in
// the language of the lang parameter the page adds to shared links, else Accept-Language.

var (
	// headRe matches the title and description of index.html, which the tags replace.
	headRe = regexp.MustCompile(`<title>[^<]*</title>\s*<meta name="description" content="[^"]*">`)
	// i18nRe matches a string of the translation files: `  'key': 'value',`.
	i18nRe = regexp.MustCompile(`(?m)^  '([^']+)': '((?:[^'\\]|\\.)*)',$`)
)

// uiStrings holds the page translations by language, read once from the embedded files.
type uiStrings struct {
	once  sync.Once
	langs map[string]map[string]string
}

func (u *uiStrings) load(fsys fs.FS) map[string]map[string]string {
	u.once.Do(func() {
		u.langs = map[string]map[string]string{}
		files, _ := fs.Glob(fsys, "i18n/*.js")
		unescape := strings.NewReplacer(`\'`, `'`, `\\`, `\`, `\n`, "\n")
		for _, f := range files {
			body, err := fs.ReadFile(fsys, f)
			if err != nil {
				continue
			}
			m := map[string]string{}
			for _, s := range i18nRe.FindAllStringSubmatch(string(body), -1) {
				m[s[1]] = unescape.Replace(s[2])
			}
			u.langs[strings.TrimSuffix(strings.TrimPrefix(f, "i18n/"), ".js")] = m
		}
	})
	return u.langs
}

// shareLang picks the language of a preview.
func (s *Server) shareLang(r *http.Request) string {
	langs := s.ui.load(s.web)
	if l := r.URL.Query().Get("lang"); langs[l] != nil {
		return l
	}
	// "uk-UA,uk;q=0.9,en;q=0.8": browsers list languages by preference.
	for part := range strings.SplitSeq(r.Header.Get("Accept-Language"), ",") {
		tag, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		base, _, _ := strings.Cut(strings.ToLower(tag), "-")
		if langs[base] != nil {
			return base
		}
	}
	return "en"
}

// text returns a translated string with {name} placeholders filled, English when missing.
func (s *Server) text(lang, key string, vars ...any) string {
	langs := s.ui.load(s.web)
	v, ok := langs[lang][key]
	if !ok {
		v = langs["en"][key]
	}
	for i := 0; i+1 < len(vars); i += 2 {
		v = strings.ReplaceAll(v, "{"+fmt.Sprint(vars[i])+"}", fmt.Sprint(vars[i+1]))
	}
	return v
}

// sharePage serves index.html with preview tags for player and mission links and leaves
// every other request to the static handler.
func (s *Server) sharePage(static *staticHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		index, ok := static.files["/index.html"]
		if (r.URL.Path != "/" && r.URL.Path != "/index.html") || (!q.Has("player") && !q.Has("mission")) || !ok ||
			(r.Method != http.MethodGet && r.Method != http.MethodHead) {
			static.ServeHTTP(w, r)
			return
		}
		lang := s.shareLang(r)
		title, desc := s.preview(r, lang)
		site := s.cfg.UI.Title
		if site == "" {
			site = "OCAP Statistics"
		}
		body := index.body
		if title != "" {
			body = headRe.ReplaceAllLiteral(body, previewTags(site, title+" — "+site, desc, s.cfg.UI.LogoURL))
		}
		hdr := w.Header()
		hdr.Set("Content-Type", "text/html; charset=utf-8")
		hdr.Set("Content-Security-Policy", cspSite)
		hdr.Set("Cache-Control", "no-cache")
		hdr.Add("Vary", "Accept-Language")
		writeBody(w, r, body, nil)
	})
}

// preview returns the title and description of the player or mission a link opens, or ""
// when there is none.
func (s *Server) preview(r *http.Request, lang string) (title, desc string) {
	ctx, q := r.Context(), r.URL.Query()
	if id, err := strconv.ParseInt(q.Get("mission"), 10, 64); err == nil {
		return s.missionPreview(ctx, lang, id)
	}
	name := q.Get("player")
	f, err := s.filter(r)
	if name == "" || err != nil {
		return "", ""
	}
	b, err := s.report(ctx, f)
	if err != nil {
		return "", ""
	}
	p, ok := b.res.FindPlayer(b.ids.Name(name, ""))
	if !ok {
		return "", ""
	}
	// The squad tag goes before the name, as in the game.
	title = p.Name
	if p.SquadRaw != "" {
		title = "[" + p.SquadRaw + "] " + p.Name
	}
	period := s.text(lang, "period.all")
	if f.From != "0000-01-01" || f.To != "9999-12-31" {
		period = strings.TrimSpace(strings.TrimPrefix(f.From, "0000-01-01") + " – " + strings.TrimSuffix(f.To, "9999-12-31"))
	}
	stat := func(key string, v any) string { return s.text(lang, key) + ": " + fmt.Sprint(v) }
	desc = strings.Join([]string{period, stat("tile.missions", p.Attendance), stat("col.kills", p.Kills), stat("col.deaths", p.Deaths),
		stat("col.kd", strconv.FormatFloat(p.KD, 'f', 2, 64)), stat("tile.assists", p.KillAssists)}, " · ")
	return title, desc
}

func (s *Server) missionPreview(ctx context.Context, lang string, id int64) (title, desc string) {
	m, err := s.store.Mission(ctx, id)
	if err != nil {
		return "", ""
	}
	rows, err := s.store.MissionRows(ctx, id)
	if err != nil {
		return "", ""
	}
	kills := 0
	for _, row := range rows {
		kills += row.Kills
	}
	result := s.text(lang, "mission.noWinner")
	if m.EndSide != "" {
		result = s.text(lang, "mission.winner", "side", m.EndSide)
		if m.EndMessage != "" {
			result += " (" + m.EndMessage + ")"
		}
	}
	desc = strings.Join([]string{m.Date, m.World, s.text(lang, "duration", "m", int(m.Duration)/60, "s", int(m.Duration)%60), result,
		s.text(lang, "col.players") + ": " + strconv.Itoa(len(rows)), s.text(lang, "col.kills") + ": " + strconv.Itoa(kills)}, " · ")
	return m.Name, desc
}

// previewTags renders the title, description and Open Graph tags, escaped.
func previewTags(site, title, desc, image string) []byte {
	var b bytes.Buffer
	e := html.EscapeString
	fmt.Fprintf(&b, "<title>%s</title>\n  <meta name=\"description\" content=\"%s\">\n", e(title), e(desc))
	fmt.Fprintf(&b, "  <meta property=\"og:type\" content=\"website\">\n  <meta property=\"og:site_name\" content=\"%s\">\n", e(site))
	fmt.Fprintf(&b, "  <meta property=\"og:title\" content=\"%s\">\n  <meta property=\"og:description\" content=\"%s\">\n", e(title), e(desc))
	// Only an absolute address works as a preview image.
	if strings.HasPrefix(image, "https://") || strings.HasPrefix(image, "http://") {
		fmt.Fprintf(&b, "  <meta property=\"og:image\" content=\"%s\">\n", e(image))
	}
	b.WriteString(`  <meta name="twitter:card" content="summary">`)
	return b.Bytes()
}
