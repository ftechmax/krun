package build

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

const dockerignoreFileName = ".dockerignore"

type ignorePattern struct {
	negated bool
	re      *regexp.Regexp
}

type ignoreMatcher struct {
	patterns     []ignorePattern
	hasNegations bool
}

// loadDockerignore reads the .dockerignore at the root of the project being
// synced. A missing file yields a nil matcher, which matches nothing.
func loadDockerignore(root string) (*ignoreMatcher, error) {
	f, err := os.Open(filepath.Join(filepath.FromSlash(root), dockerignoreFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	return parseDockerignore(f)
}

func parseDockerignore(r io.Reader) (*ignoreMatcher, error) {
	m := &ignoreMatcher{}

	scanner := bufio.NewScanner(r)
	firstLine := true
	for scanner.Scan() {
		line := scanner.Text()
		if firstLine {
			line = strings.TrimPrefix(line, "\uFEFF")
			firstLine = false
		}

		line = strings.TrimSpace(strings.ReplaceAll(line, `\`, "/"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		source := line
		p := ignorePattern{}
		if strings.HasPrefix(line, "!") {
			line = strings.TrimSpace(line[1:])
			if line == "" {
				return nil, fmt.Errorf("illegal exclusion pattern in %s: %q", dockerignoreFileName, source)
			}
			p.negated = true
			m.hasNegations = true
		}

		line = strings.TrimPrefix(path.Clean(line), "/")
		if line == "" || line == "." {
			continue
		}

		re, err := compileIgnorePattern(line)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern %q in %s: %w", source, dockerignoreFileName, err)
		}
		p.re = re

		m.patterns = append(m.patterns, p)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return m, nil
}

// compileIgnorePattern converts a cleaned pattern into a regexp anchored to a
// full slash-separated path: "*" and "?" stop at a separator, "**" spans any
// number of path segments.
func compileIgnorePattern(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")

	for i := 0; i < len(pattern); i++ {
		switch ch := pattern[i]; ch {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				// Treat "**/" as "**" so the separator is not required.
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
				}
				if i+1 == len(pattern) {
					// Trailing "**" matches all remaining characters.
					b.WriteString(".*")
				} else {
					// Any number of leading path segments, including none.
					b.WriteString("(.*/)?")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}

	b.WriteString("$")
	return regexp.Compile(b.String())
}

// matches reports whether a slash-separated path is excluded, taking parent
// directory matches into account so that "obj" also excludes "obj/app.dll".
func (m *ignoreMatcher) matches(relPath string) bool {
	if m == nil {
		return false
	}

	relPath = path.Clean(relPath)
	if relPath == "." {
		return false
	}

	segments := strings.Split(relPath, "/")
	matched := false
	for _, p := range m.patterns {
		match := p.re.MatchString(relPath)
		for i := 1; !match && i < len(segments); i++ {
			match = p.re.MatchString(strings.Join(segments[:i], "/"))
		}

		if match {
			matched = !p.negated
		}
	}

	return matched
}

// Negations may re-include nested files, so keep walking those directories.
func (m *ignoreMatcher) canSkipDir(relPath string) bool {
	if m == nil || m.hasNegations {
		return false
	}
	return m.matches(relPath)
}
