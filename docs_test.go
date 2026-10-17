// Package ocapstatistics_test keeps the contributor documentation honest: every file, make
// target and Go identifier that the agent guides mention must exist, so the guides cannot
// silently drift away from the code.
package ocapstatistics_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// guides are the agent guides checked by these tests.
var guides = []string{"AGENTS.md", "web/AGENTS.md"}

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestAgentsMapPaths checks the repository map of AGENTS.md.
func TestAgentsMapPaths(t *testing.T) {
	doc := readFile(t, "AGENTS.md")
	block := regexp.MustCompile("(?s)## Repository map\\s*```\\n(.*?)```").FindStringSubmatch(doc)
	if block == nil {
		t.Fatal("AGENTS.md has no repository map block")
	}
	checked := 0
	for line := range strings.SplitSeq(block[1], "\n") {
		field := strings.Fields(line)
		if len(field) == 0 {
			continue
		}
		// "web/static/index.html, app.js" lists files of one directory.
		first := strings.TrimSuffix(field[0], ",")
		paths := []string{first}
		if len(field) > 1 && strings.HasSuffix(field[0], ",") {
			paths = append(paths, filepath.Join(filepath.Dir(first), strings.TrimSuffix(field[1], ",")))
		}
		for _, p := range paths {
			checked++
			if _, err := os.Stat(strings.TrimSuffix(p, "/")); err != nil {
				t.Errorf("AGENTS.md repository map: %s does not exist", p)
			}
		}
	}
	if checked < 20 {
		t.Fatalf("checked only %d paths; did the map format change?", checked)
	}
}

// TestAgentsMakeTargets checks that every `make <target>` in the guides exists.
func TestAgentsMakeTargets(t *testing.T) {
	makefile := readFile(t, "Makefile")
	found := 0
	for _, guide := range guides {
		for _, m := range regexp.MustCompile("`make ([a-z-]+)").FindAllStringSubmatch(readFile(t, guide), -1) {
			found++
			if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(m[1]) + `:`).MatchString(makefile) {
				t.Errorf("%s mentions `make %s`, but the Makefile has no such target", guide, m[1])
			}
		}
	}
	if found == 0 {
		t.Fatal("no make targets found in the guides")
	}
}

// TestAgentsPaths checks paths written in backticks (`dir/file.ext`, `dir/*.js`): they must
// exist relative to the guide or to the repository root.
func TestAgentsPaths(t *testing.T) {
	re := regexp.MustCompile("`([A-Za-z0-9_.*-]+/[A-Za-z0-9_./*-]*)`")
	checked := 0
	for _, guide := range guides {
		for _, m := range re.FindAllStringSubmatch(readFile(t, guide), -1) {
			p := m[1]
			first, _, _ := strings.Cut(p, "/")
			if strings.Contains(first, ".") && !strings.HasPrefix(first, ".") {
				continue // module path such as example.org/pkg
			}
			checked++
			found := false
			for _, base := range []string{filepath.Dir(guide), "."} {
				if matches, _ := filepath.Glob(filepath.Join(base, strings.TrimSuffix(p, "/"))); len(matches) > 0 {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s mentions %s, which does not exist", guide, p)
			}
		}
	}
	if checked < 20 {
		t.Fatalf("checked only %d paths; did the guide style change?", checked)
	}
}

// TestAgentsIdentifiers checks Go identifiers written as `pkg.Name` in the guides: the name
// must appear in that package's source.
func TestAgentsIdentifiers(t *testing.T) {
	var doc string
	for _, guide := range guides {
		doc += readFile(t, guide)
	}
	pkgs := map[string]string{}
	_ = filepath.WalkDir("internal", func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && p != "internal" {
			pkgs[d.Name()] = p
		}
		return nil
	})
	re := regexp.MustCompile("`[^`]*?\\b([a-z]+)\\.([A-Z][A-Za-z0-9]*)")
	checked := 0
	for _, m := range re.FindAllStringSubmatch(doc, -1) {
		dir, ok := pkgs[m[1]]
		if !ok {
			continue
		}
		checked++
		found := false
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".go") && regexp.MustCompile(`\b`+m[2]+`\b`).MatchString(readFile(t, filepath.Join(dir, e.Name()))) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("a guide mentions %s.%s, which does not exist in %s", m[1], m[2], dir)
		}
	}
	if checked < 10 {
		t.Fatalf("checked only %d identifiers; did the guide style change?", checked)
	}
}
