// Package ocapstatistics_test keeps the contributor documentation honest: every file, make
// target and Go identifier that AGENTS.md mentions must exist, so the guide cannot silently
// drift away from the code.
package ocapstatistics_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

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

// TestAgentsMakeTargets checks that every `make <target>` in AGENTS.md exists.
func TestAgentsMakeTargets(t *testing.T) {
	doc, makefile := readFile(t, "AGENTS.md"), readFile(t, "Makefile")
	targets := regexp.MustCompile("`make ([a-z-]+)`").FindAllStringSubmatch(doc, -1)
	if len(targets) == 0 {
		t.Fatal("no make targets found in AGENTS.md")
	}
	for _, m := range targets {
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(m[1]) + `:`).MatchString(makefile) {
			t.Errorf("AGENTS.md mentions `make %s`, but the Makefile has no such target", m[1])
		}
	}
}

// TestAgentsIdentifiers checks Go identifiers written as `pkg.Name` in AGENTS.md: the name
// must appear in that package's source.
func TestAgentsIdentifiers(t *testing.T) {
	doc := readFile(t, "AGENTS.md")
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
			t.Errorf("AGENTS.md mentions %s.%s, which does not exist in %s", m[1], m[2], dir)
		}
	}
	if checked < 10 {
		t.Fatalf("checked only %d identifiers; did the AGENTS.md style change?", checked)
	}
}
