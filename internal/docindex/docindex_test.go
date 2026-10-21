// Package docindex holds the check that the README docs tables stay in sync
// with the files under docs/. A document that nobody can find from the front
// page does not exist for a reader, so CI fails when a doc is added without a
// README row or when a row links a file that is not there.
package docindex

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// linkRe matches markdown inline links: [text](target).
var linkRe = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)

// repoRoot walks up from the package directory until it finds a directory
// that contains both README.md and docs/.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "README.md")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "docs")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (a directory containing README.md and docs/)")
		}
		dir = parent
	}
}

// readDocsMarkdown returns every .md file under docs/, slash-separated and
// relative to the repo root (e.g. "docs/i18n/es/glossary.md").
func readDocsMarkdown(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.Walk(filepath.Join(root, "docs"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("rel %s to %s: %w", root, path, err)
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs/: %v", err)
	}
	sort.Strings(files)
	return files
}

// readDocsLinks extracts every link target in README.md that points into
// docs/, normalized to a repo-root-relative path with any anchor and any
// trailing slash stripped.
func readDocsLinks(t *testing.T, root string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	var links []string
	for _, m := range linkRe.FindAllStringSubmatch(string(data), -1) {
		target := m[1]
		if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
			continue
		}
		target = strings.TrimPrefix(target, "./")
		if !strings.HasPrefix(target, "docs/") {
			continue
		}
		if i := strings.IndexAny(target, "#?"); i >= 0 {
			target = target[:i]
		}
		target = strings.TrimSuffix(target, "/")
		links = append(links, target)
	}
	return links
}

// TestREADMEDocsLinksResolve fails when a README link into docs/ points at
// nothing.
func TestREADMEDocsLinksResolve(t *testing.T) {
	root := repoRoot(t)
	for _, link := range readDocsLinks(t, root) {
		if _, err := os.Stat(filepath.Join(root, link)); err != nil {
			t.Errorf("README.md links %s, which does not exist", link)
		}
	}
}

// TestEveryDocReachableFromREADME fails when a .md file under docs/ is not
// linked from README.md. A file inside a subdirectory may instead be linked
// from that subdirectory's README.md, provided README.md links the index.
// docs/i18n/ is handled that way: one row in the README points at the index,
// and the index links every translation.
func TestEveryDocReachableFromREADME(t *testing.T) {
	root := repoRoot(t)
	readme := readDocsLinks(t, root)
	linked := make(map[string]bool, len(readme))
	for _, link := range readme {
		linked[link] = true
	}

	// readFile returns the content of a repo-root-relative path.
	readFile := func(rel string) string {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return ""
		}
		return string(data)
	}

	var orphans []string
	for _, doc := range readDocsMarkdown(t, root) {
		if linked[doc] {
			continue
		}
		// A subdirectory file counts as reachable when its nearest ancestor
		// directory index links it and README.md links that index.
		if coveredByIndex(doc, linked, readFile) {
			continue
		}
		orphans = append(orphans, doc)
	}
	if len(orphans) > 0 {
		t.Errorf("%d orphan document(s), linked from no README table:\n  %s",
			len(orphans), strings.Join(orphans, "\n  "))
	}
}

// coveredByIndex reports whether doc is linked from a docs/<dir>/README.md
// index that README.md itself links. The child is matched by its path
// relative to the index directory (e.g. "es/glossary.md").
func coveredByIndex(doc string, linked map[string]bool, readFile func(string) string) bool {
	dir := filepath.ToSlash(filepath.Dir(doc))
	for dir != "docs" && dir != "." {
		index := dir + "/README.md"
		if linked[index] {
			content := readFile(index)
			// Children may be linked relative to the index ("es/glossary.md").
			if strings.Contains(content, "](./"+strings.TrimPrefix(doc, dir+"/")+")") ||
				strings.Contains(content, "]("+strings.TrimPrefix(doc, dir+"/")+")") {
				return true
			}
		}
		dir = filepath.ToSlash(filepath.Dir(dir))
	}
	return false
}
