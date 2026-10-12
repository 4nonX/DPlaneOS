package gitops

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every state.yaml example in the documentation uses settings that exist.
// (The examples named sections and keys the parser skipped without a word;
// a reader who copied them got a file that did less than it said.)
func TestDocumentedExamplesUseKnownSettings(t *testing.T) {
	sections := map[string]bool{}
	for _, k := range []string{"version", "ignore_extraneous", "pools", "datasets", "shares", "nfs", "stacks", "system",
		"users", "groups", "replication", "ldap", "smart_tasks", "fabrics"} {
		sections[k] = true
	}
	block := regexp.MustCompile("(?s)```yaml\n(.*?)```")
	topKey := regexp.MustCompile(`(?m)^([a-z_]+):`)
	checked := 0
	for _, doc := range []string{
		"../../../docs/reference/GITOPS-REFERENCE.md",
		"../../../docs/admin/GITOPS-DRIVEN-NAS.md",
	} {
		content, err := os.ReadFile(doc)
		if err != nil {
			t.Fatalf("%s: %v", doc, err)
		}
		for _, m := range block.FindAllStringSubmatch(string(content), -1) {
			example := m[1]
			keys := topKey.FindAllStringSubmatch(example, -1)
			if len(keys) == 0 {
				continue
			}
			// An example of something else (a CI workflow, a compose file).
			isState := true
			for _, k := range keys {
				if !sections[k[1]] {
					isState = false
				}
			}
			if !isState {
				if strings.Contains(example, "version:") && strings.Contains(example, "datasets:") {
					t.Errorf("%s: an example of state.yaml has a section that does not exist:\n%s", doc, example)
				}
				continue
			}
			raw, err := (&yamlParser{lines: splitLines(example)}).parseDocument()
			if err != nil {
				t.Errorf("%s: example does not parse: %v\n%s", doc, err, example)
				continue
			}
			if unknown := unknownKeys(raw); len(unknown) > 0 {
				t.Errorf("%s: example uses settings that do not exist: %v\n%s", doc, unknown, example)
			}
			// A complete file (it has a version) must be one the system takes.
			if strings.Contains(example, "version:") && !strings.Contains(example, "[]") {
				if _, err := ParseStateYAML(example); err != nil {
					t.Errorf("%s: a complete example is refused: %v\n%s", doc, err, example)
				}
			}
			checked++
		}
	}
	if checked < 10 {
		t.Errorf("only %d examples found: has the documentation moved?", checked)
	}
}
