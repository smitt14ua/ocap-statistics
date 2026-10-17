package web

import (
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/smitt14ua/ocap-statistics/internal/report"
)

var (
	keyRe   = regexp.MustCompile(`(?m)^  '([^']+)':`)
	langsRe = regexp.MustCompile(`const LANGS = \{([^}]*)\}`)
	// Keys used by markup and scripts: data-i18n* attributes, t('key') calls and
	// 'data-i18n…': 'key' properties of el() calls.
	usedRes = []*regexp.Regexp{
		regexp.MustCompile(`data-i18n(?:-placeholder|-title|-aria)?="([^"]+)"`),
		regexp.MustCompile(`\bt\('([a-zA-Z0-9_.]+)'`),
		regexp.MustCompile(`'data-i18n(?:-title|-aria)?': '([^']+)'`),
		regexp.MustCompile(`data-confirm="([^"]+)"`),
	}
)

// translations returns the keys of every language file in i18n/, by language code.
func translations(t *testing.T) map[string][]string {
	t.Helper()
	files, err := fs.Glob(FS(), "i18n/*.js")
	if err != nil {
		t.Fatal(err)
	}
	langs := map[string][]string{}
	for _, f := range files {
		src, err := fs.ReadFile(FS(), f)
		if err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, k := range keyRe.FindAllStringSubmatch(string(src), -1) {
			keys = append(keys, k[1])
		}
		slices.Sort(keys)
		langs[strings.TrimSuffix(path.Base(f), ".js")] = keys
	}
	return langs
}

// TestTranslationsComplete keeps every language in sync with English.
func TestTranslationsComplete(t *testing.T) {
	langs := translations(t)
	for _, want := range []string{"en", "cs", "de", "pl", "uk"} {
		if _, ok := langs[want]; !ok {
			t.Fatalf("language file i18n/%s.js missing", want)
		}
	}
	en := langs["en"]
	if len(en) < 200 { // guards the parser: an empty match would make this test pass vacuously
		t.Fatalf("parsed only %d English keys; did the i18n/en.js layout change?", len(en))
	}
	for lang, keys := range langs {
		if dup := duplicates(keys); len(dup) > 0 {
			t.Errorf("%s: duplicate keys %v", lang, dup)
		}
		if missing := diff(en, keys); len(missing) > 0 {
			t.Errorf("%s: missing keys %v", lang, missing)
		}
		if extra := diff(keys, en); len(extra) > 0 {
			t.Errorf("%s: keys not in en %v", lang, extra)
		}
	}
}

// TestLanguagesWired checks that every language file is listed in i18n.js and loaded by every
// page before i18n.js, so a new language cannot be half added.
func TestLanguagesWired(t *testing.T) {
	core, err := fs.ReadFile(FS(), "i18n.js")
	if err != nil {
		t.Fatal(err)
	}
	m := langsRe.FindSubmatch(core)
	if m == nil {
		t.Fatal("i18n.js has no LANGS table")
	}
	for lang := range translations(t) {
		if !strings.Contains(string(m[1]), lang+":") {
			t.Errorf("i18n/%s.js is not listed in LANGS of i18n.js", lang)
		}
		for page, prefix := range map[string]string{"index.html": "", "admin/index.html": "../"} {
			src, err := fs.ReadFile(FS(), page)
			if err != nil {
				t.Fatal(err)
			}
			tag := `<script src="` + prefix + `i18n/` + lang + `.js"></script>`
			at, coreAt := strings.Index(string(src), tag), strings.Index(string(src), `<script src="`+prefix+`i18n.js"></script>`)
			if at < 0 || coreAt < 0 || at > coreAt {
				t.Errorf("%s must load %s before i18n.js", page, tag)
			}
		}
	}
}

// TestUsedKeysExist fails when markup or scripts use a key that the language files do not define.
func TestUsedKeysExist(t *testing.T) {
	en := translations(t)["en"]
	used := 0
	err := fs.WalkDir(FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(p, "i18n/") || !(strings.HasSuffix(p, ".html") || strings.HasSuffix(p, ".js")) {
			return err
		}
		src, err := fs.ReadFile(FS(), p)
		if err != nil {
			return err
		}
		for _, re := range usedRes {
			for _, m := range re.FindAllStringSubmatch(string(src), -1) {
				used++
				if !slices.Contains(en, m[1]) {
					t.Errorf("%s uses undefined translation key %q", p, m[1])
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if used < 200 {
		t.Fatalf("found only %d key usages; did the markup conventions change?", used)
	}
	// Formula variables are described in the admin panel help (var.<name>).
	for _, v := range report.Variables {
		if !slices.ContainsFunc(en, func(k string) bool { return strings.EqualFold(k, "var."+v) }) {
			t.Errorf("formula variable %q has no var.%s translation", v, v)
		}
	}
}

func diff(a, b []string) []string {
	var out []string
	for _, k := range a {
		if !slices.Contains(b, k) {
			out = append(out, k)
		}
	}
	return out
}

func duplicates(sorted []string) []string {
	var out []string
	for i := 1; i < len(sorted); i++ {
		if sorted[i] == sorted[i-1] {
			out = append(out, sorted[i])
		}
	}
	return out
}
