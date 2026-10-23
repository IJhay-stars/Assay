package main_test

import (
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestDocLinks checks every relative link and #anchor in README.md and
// docs/**/*.md (#312).
//
// The docs are the product's argument, and they cross-reference each other by
// path and heading anchor (deployment.md#entry-lifetime,
// checks.md#sep1-domain). A renamed heading or moved file breaks a link
// silently, because GitHub renders the page just fine until someone clicks.
//
// The check is deliberately offline: network URLs are skipped rather than
// fetched, so the test is deterministic and can run in the normal `go test`
// pass. Anchors are generated the way GitHub generates them, so a link that
// works in the rendered docs is the link this test accepts.
func TestDocLinks(t *testing.T) {
	failures, err := docLinkFailures(getRepoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range failures {
		t.Error(failure)
	}
}

// TestDocLinksDetectsBrokenLinks keeps the checker honest: a link checker that
// cannot fail is worse than none, because it reads as coverage.
func TestDocLinksDetectsBrokenLinks(t *testing.T) {
	repoRoot := t.TempDir()
	readme := "# Readme\n\n" +
		"[good](docs/good.md#a-heading)\n" +
		"[missing file](docs/missing.md)\n" +
		"[missing anchor](docs/good.md#not-a-heading)\n" +
		"[external](https://example.com/not-fetched)\n" +
		"[good anchor elsewhere](docs/other.md#explicit)\n\n" +
		"```sh\n" +
		"[inside a fence](docs/also-missing.md)\n" +
		"```\n\n" +
		"`[inside a code span](docs/still-missing.md)`\n"
	writeDoc(t, repoRoot, "README.md", readme)
	writeDoc(t, repoRoot, "docs/good.md", "## A heading\n")
	writeDoc(t, repoRoot, "docs/other.md", "<a id=\"explicit\"></a>\n\n## A heading\n")

	failures, err := docLinkFailures(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 2 {
		t.Fatalf("expected 2 failures (missing file and anchor), got %d:\n%s",
			len(failures), strings.Join(failures, "\n"))
	}
	for _, want := range []string{"docs/missing.md", "docs/good.md#not-a-heading"} {
		found := false
		for _, failure := range failures {
			if strings.Contains(failure, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("expected a failure mentioning %q, got:\n%s", want, strings.Join(failures, "\n"))
		}
	}
}

func writeDoc(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func docLinkFailures(repoRoot string) ([]string, error) {
	files, err := markdownFiles(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("collect markdown files: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no markdown files found to check")
	}

	var failures []string
	fail := func(format string, args ...any) {
		failures = append(failures, fmt.Sprintf(format, args...))
	}

	anchors := make(map[string]map[string]bool)
	anchorSet := func(path string) (map[string]bool, error) {
		if set, ok := anchors[path]; ok {
			return set, nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		set := markdownAnchors(string(b))
		anchors[path] = set
		return set, nil
	}

	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			fail("read %s: %v", relPath(repoRoot, file), err)
			continue
		}
		for _, link := range extractLinks(string(content)) {
			target, fragment := splitLink(link.target)
			if !isLocalLink(target) {
				continue // external URLs are explicitly not fetched
			}

			where := fmt.Sprintf("%s:%d: %s", relPath(repoRoot, file), link.line, link.target)

			resolved := file
			if target != "" {
				p, err := url.PathUnescape(target)
				if err != nil {
					fail("%s: invalid path escape: %v", where, err)
					continue
				}
				resolved = filepath.Clean(filepath.Join(filepath.Dir(file), filepath.FromSlash(p)))
				if !withinRoot(repoRoot, resolved) {
					fail("%s: link escapes the repository", where)
					continue
				}
				info, err := os.Stat(resolved)
				if err != nil {
					fail("%s: target does not exist", where)
					continue
				}
				if info.IsDir() {
					if fragment != "" {
						fail("%s: directory link cannot carry an anchor", where)
					}
					continue
				}
			}

			if fragment == "" {
				continue
			}
			if !strings.HasSuffix(resolved, ".md") {
				fail("%s: anchor on a non-markdown file", where)
				continue
			}
			frag, err := url.PathUnescape(fragment)
			if err != nil {
				fail("%s: invalid anchor escape: %v", where, err)
				continue
			}
			set, err := anchorSet(resolved)
			if err != nil {
				fail("%s: read anchor target: %v", where, err)
				continue
			}
			if !set[frag] {
				fail("%s: no heading or id %q in %s", where, frag, relPath(repoRoot, resolved))
			}
		}
	}
	return failures, nil
}

// markdownFiles returns README.md and every markdown file under docs/.
func markdownFiles(repoRoot string) ([]string, error) {
	files := []string{filepath.Join(repoRoot, "README.md")}
	err := filepath.WalkDir(filepath.Join(repoRoot, "docs"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".md") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk docs: %w", err)
	}
	return files, nil
}

func relPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

type docLink struct {
	target string
	line   int
}

var (
	linkRe = regexp.MustCompile(`\]\(\s*(<[^>]*>|[^)\s]+)(?:\s+["'][^)]*["'])?\s*\)`)
	idRe   = regexp.MustCompile(`<a\s+[^>]*(?:id|name)\s*=\s*["']([^"']+)["']`)
)

// extractLinks returns inline Markdown links and images, skipping fenced code
// blocks, inline code spans, and HTML comments. Only the destination is kept;
// the text is irrelevant to the check.
func extractLinks(content string) []docLink {
	var links []docLink
	inFence := false
	fence := ""
	inComment := false

	for i, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)

		if inComment {
			if idx := strings.Index(trimmed, "-->"); idx >= 0 {
				trimmed = trimmed[idx+3:]
				inComment = false
			} else {
				continue
			}
		}
		for {
			start := strings.Index(trimmed, "<!--")
			if start < 0 {
				break
			}
			if end := strings.Index(trimmed[start+4:], "-->"); end >= 0 {
				trimmed = trimmed[:start] + strings.Repeat(" ", end+7) + trimmed[start+4+end+3:]
				continue
			}
			trimmed = trimmed[:start]
			inComment = true
			break
		}

		if inFence {
			if strings.HasPrefix(trimmed, fence) {
				inFence = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = trimmed[:3]
			inFence = true
			continue
		}

		// Markdown inside a code span is text, not a link.
		lineText := regexp.MustCompile("`[^`]*`").ReplaceAllString(trimmed, "")
		for _, m := range linkRe.FindAllStringSubmatch(lineText, -1) {
			dest := strings.TrimSpace(m[1])
			dest = strings.TrimPrefix(strings.TrimSuffix(dest, ">"), "<")
			links = append(links, docLink{target: dest, line: i + 1})
		}
	}
	return links
}

// splitLink separates a URL into its path and fragment. Query strings are
// dropped: they do not name a different file.
func splitLink(link string) (target, fragment string) {
	if idx := strings.IndexByte(link, '#'); idx >= 0 {
		fragment = link[idx+1:]
		link = link[:idx]
	}
	if idx := strings.IndexByte(link, '?'); idx >= 0 {
		link = link[:idx]
	}
	return link, fragment
}

func isLocalLink(target string) bool {
	if target == "" {
		return true // same-document anchor
	}
	if strings.HasPrefix(target, "//") {
		return false
	}
	// Any scheme (https:, mailto:, tel:, data:, ...) names something this
	// offline check must not fetch.
	return !regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`).MatchString(target)
}

var (
	headingRe     = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	explicitIDRe  = regexp.MustCompile(`\s*\{#([^}]+)\}\s*$`)
	imageRe       = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	mdLinkRe      = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	htmlTagRe     = regexp.MustCompile(`<[^>]+>`)
	emphasisRe    = regexp.MustCompile("[*~]")
	slugDropRe    = regexp.MustCompile(`[^\p{L}\p{N}\p{M}_\- ]+`)
	htmlAnchorRe  = idRe
	displayedText = strings.NewReplacer("`", "")
)

// markdownAnchors returns every anchor a GitHub-rendered page would expose:
// one per heading (with GitHub's duplicate suffixing) plus any explicit
// <a id="...">/<a name="..."> in the document.
func markdownAnchors(content string) map[string]bool {
	anchors := make(map[string]bool)
	seen := make(map[string]int)
	inFence := false
	fence := ""

	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)

		if inFence {
			if strings.HasPrefix(trimmed, fence) {
				inFence = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = trimmed[:3]
			inFence = true
			continue
		}

		for _, m := range htmlAnchorRe.FindAllStringSubmatch(line, -1) {
			anchors[m[1]] = true
		}

		m := headingRe.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		text := m[2]
		if id := explicitIDRe.FindStringSubmatch(text); id != nil {
			anchors[id[1]] = true
			text = explicitIDRe.ReplaceAllString(text, "")
		}
		slug := githubSlug(text)
		if slug == "" {
			continue
		}
		if n, ok := seen[slug]; ok {
			anchors[fmt.Sprintf("%s-%d", slug, n)] = true
			seen[slug] = n + 1
		} else {
			anchors[slug] = true
			seen[slug] = 1
		}
	}
	return anchors
}

// githubSlug renders heading text to the anchor GitHub generates: lowercase,
// markup removed, punctuation dropped, spaces replaced by hyphens.
func githubSlug(text string) string {
	text = imageRe.ReplaceAllString(text, "$1")
	text = mdLinkRe.ReplaceAllString(text, "$1")
	text = htmlTagRe.ReplaceAllString(text, "")
	text = displayedText.Replace(text)
	text = emphasisRe.ReplaceAllString(text, "")
	text = strings.ToLower(text)
	text = slugDropRe.ReplaceAllString(text, "")
	text = strings.TrimSpace(text)
	text = strings.ReplaceAll(text, " ", "-")
	return text
}
