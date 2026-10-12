package security

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every command the daemon runs with fixed arguments through the allowlist
// is one the allowlist accepts.
//
// A rule that refuses what the daemon itself asks for does not show as an
// error where callers treat a failed command as "no data": the support
// bundle held a refusal instead of the log, and the health page reported a
// synchronised clock it had never been allowed to look at. This reads the
// call sites from the source and validates each.
func TestFixedCommandCallsPassTheAllowlist(t *testing.T) {
	call := regexp.MustCompile(`(?s)(?:cmdutil\.Run\w*|executeCommand\w*)\(([^()]*(?:\([^()]*\)[^()]*)*)\)`)
	lit := regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	notArgs := regexp.MustCompile(`\[\]string\{|\}|cmdutil\.Timeout\w+|Timeout\w+|\d+\s*\*\s*time\.\w+|time\.\w+|\s|,`)

	checked := 0
	err := filepath.Walk("..", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range call.FindAllSubmatchIndex(src, -1) {
			args := string(src[m[2]:m[3]])
			var lits []string
			for _, l := range lit.FindAllStringSubmatch(args, -1) {
				lits = append(lits, l[1])
			}
			if len(lits) == 0 {
				continue
			}
			// Only calls whose arguments are all written out.
			if rest := notArgs.ReplaceAllString(lit.ReplaceAllString(args, ""), ""); rest != "" {
				continue
			}
			if _, listed := CommandWhitelist[lits[0]]; !listed {
				continue
			}
			checked++
			if err := ValidateCommand(lits[0], lits[1:]); err != nil {
				line := 1 + strings.Count(string(src[:m[0]]), "\n")
				t.Errorf("%s:%d: %v is refused by the allowlist: %v", filepath.ToSlash(path), line, lits, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 30 {
		t.Errorf("only %d call sites found: has the way commands are run changed?", checked)
	}
}
