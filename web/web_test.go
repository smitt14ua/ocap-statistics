package web

import (
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/smitt14ua/ocap-statistics/internal/report"
)

var (
	langBlockRe = regexp.MustCompile(`(?ms)^    (\w\w): \{\n(.*?)^    \},`)
	keyRe       = regexp.MustCompile(`(?m)^      '([^']+)':`)
	// Keys used by markup and scripts: data-i18n* attributes, t('key') calls and
	// 'data-i18n…': 'key' properties of el() calls.
	usedRes = []*regexp.Regexp{
		regexp.MustCompile(`data-i18n(?:-placeholder|-title|-aria)?="([^"]+)"`),
		regexp.MustCompile(`\bt\('([a-zA-Z0-9_.]+)'`),
		regexp.MustCompile(`'data-i18n(?:-title|-aria)?': '([^']+)'`),
		regexp.MustCompile(`data-confirm="([^"]+)"`),
	}
)

// translations returns the keys of every language block in i18n.js.
func translations(t *testing.T) map[string][]string {
	t.Helper()
	src, err := fs.ReadFile(FS(), "i18n.js")
	if err != nil {
		t.Fatal(err)
	}
	langs := map[string][]string{}
	for _, m := range langBlockRe.FindAllStringSubmatch(string(src), -1) {
		var keys []string
		for _, k := range keyRe.FindAllStringSubmatch(m[2], -1) {
			keys = append(keys, k[1])
		}
		slices.Sort(keys)
		langs[m[1]] = keys
	}
	return langs
}

// TestTranslationsComplete keeps every language in sync with English.
func TestTranslationsComplete(t *testing.T) {
	langs := translations(t)
	for _, want := range []string{"en", "cs", "de", "pl", "uk"} {
		if _, ok := langs[want]; !ok {
			t.Fatalf("language %q missing from i18n.js", want)
		}
	}
	en := langs["en"]
	if len(en) < 200 { // guards the parser: an empty match would make this test pass vacuously
		t.Fatalf("parsed only %d English keys; did the i18n.js layout change?", len(en))
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

// TestUsedKeysExist fails when markup or scripts use a key that i18n.js does not define.
func TestUsedKeysExist(t *testing.T) {
	en := translations(t)["en"]
	used := 0
	err := fs.WalkDir(FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == "i18n.js" || !(strings.HasSuffix(p, ".html") || strings.HasSuffix(p, ".js")) {
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
