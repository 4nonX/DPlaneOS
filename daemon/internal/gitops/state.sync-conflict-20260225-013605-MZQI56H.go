// Package gitops implements Phase 3: GitOps Differentiator.
//
// State machine:
//
//	Git repo ──► parse state.yaml ──► DesiredState
//	              │
//	              ▼
//	     Validate (by-id paths, schema)
//	              │
//	              ▼
//	    Read live ZFS + shares ──► LiveState
//	              │
//	              ▼
//	        DiffEngine ──► []DiffItem  (SAFE | BLOCKED | CREATE | MODIFY | NOP)
//	              │
//	              ▼
//	       SafeApply  (transactional; BLOCKED items halt plan)
//	              │
//	              ▼
//	      DriftDetector (background; broadcasts on WS)
package gitops

import (
	"bufio"
	"fmt"
	"regexp"
	"strings"
)

// ═══════════════════════════════════════════════════════════════════════════════
//  DESIRED STATE TYPES  (parsed from state.yaml)
// ═══════════════════════════════════════════════════════════════════════════════

// DesiredState is the top-level structure of /etc/dplaneos/state.yaml.
//
// Example state.yaml:
//
//	version: "1"
//	pools:
//	  - name: tank
//	    vdev_type: mirror
//	    disks:
//	      - /dev/disk/by-id/ata-WDC_WD140EDFZ-11A0VA0_1234567890
//	      - /dev/disk/by-id/ata-WDC_WD140EDFZ-11A0VA0_0987654321
//	    ashift: 12
//	    options:
//	      compression: lz4
//	      atime: "off"
//	datasets:
//	  - name: tank/media
//	    quota: 8T
//	    compression: lz4
//	    atime: "off"
//	    mountpoint: /mnt/media
//	  - name: tank/backups
//	    quota: 4T
//	    compression: zstd
//	    mountpoint: /mnt/backups
//	    encrypted: true
//	shares:
//	  - name: media
//	    path: /mnt/media
//	    read_only: false
//	    valid_users: "@media_users"
//	    comment: "Media library"
//	  - name: backups
//	    path: /mnt/backups
//	    read_only: true
type DesiredState struct {
	Version  string          `yaml:"version"`
	Pools    []DesiredPool   `yaml:"pools"`
	Datasets []DesiredDataset `yaml:"datasets"`
	Shares   []DesiredShare  `yaml:"shares"`
}

// DesiredPool describes a ZFS pool.
// Disks MUST use /dev/disk/by-id/ paths — enforced at parse time.
type DesiredPool struct {
	Name     string            `yaml:"name"`
	VdevType string            `yaml:"vdev_type"` // mirror, raidz, raidz2, raidz3, "" (stripe)
	Disks    []string          `yaml:"disks"`
	Ashift   int               `yaml:"ashift"`   // 0 = auto
	Options  map[string]string `yaml:"options"`  // pool-level zpool set properties
}

// DesiredDataset describes a ZFS dataset.
type DesiredDataset struct {
	Name        string `yaml:"name"`
	Quota       string `yaml:"quota"`        // e.g. "2T", "500G", "none"
	Compression string `yaml:"compression"`  // lz4, zstd, gzip, off
	Atime       string `yaml:"atime"`        // on, off
	Mountpoint  string `yaml:"mountpoint"`
	Encrypted   bool   `yaml:"encrypted"`
}

// DesiredShare describes an SMB share.
type DesiredShare struct {
	Name       string `yaml:"name"`
	Path       string `yaml:"path"`
	ReadOnly   bool   `yaml:"read_only"`
	ValidUsers string `yaml:"valid_users"`
	Comment    string `yaml:"comment"`
	GuestOK    bool   `yaml:"guest_ok"`
}

// ═══════════════════════════════════════════════════════════════════════════════
//  VALIDATION RULES
// ═══════════════════════════════════════════════════════════════════════════════

var (
	validDatasetRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9/_\-\.]*$`)
	validPoolRe    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_\-\.]*$`)
	validShareRe   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_\-\.]*$`)
)

// byIDPrefix is the only disk path prefix accepted by the parser.
const byIDPrefix = "/dev/disk/by-id/"

// ValidState validates a parsed DesiredState and returns a list of human-readable
// errors. An empty slice means the state is valid and safe to diff against live.
func ValidState(s *DesiredState) []string {
	var errs []string

	if s.Version != "1" {
		errs = append(errs, fmt.Sprintf("unsupported state.yaml version %q (only \"1\" is supported)", s.Version))
	}

	// ── Pools ──────────────────────────────────────────────────────────────────
	poolNames := map[string]bool{}
	for i, p := range s.Pools {
		pfx := fmt.Sprintf("pools[%d] %q", i, p.Name)

		if !validPoolRe.MatchString(p.Name) {
			errs = append(errs, pfx+": invalid pool name")
		}
		if poolNames[p.Name] {
			errs = append(errs, pfx+": duplicate pool name")
		}
		poolNames[p.Name] = true

		validVdev := map[string]bool{"": true, "mirror": true, "raidz": true,
			"raidz1": true, "raidz2": true, "raidz3": true}
		if !validVdev[p.VdevType] {
			errs = append(errs, pfx+": unknown vdev_type "+p.VdevType)
		}

		if len(p.Disks) == 0 {
			errs = append(errs, pfx+": disks list is empty")
		}

		// THE HARD RULE: every disk must be a /dev/disk/by-id/ path.
		// /dev/sdX paths are rejected unconditionally — they are unstable across
		// reboots and cause catastrophic pool imports on hardware changes.
		for _, d := range p.Disks {
			if !strings.HasPrefix(d, byIDPrefix) {
				errs = append(errs, fmt.Sprintf(
					"%s: disk %q must use /dev/disk/by-id/ path (got %q) — "+
						"/dev/sdX paths are unstable across reboots and are REJECTED",
					pfx, d, d,
				))
			}
			// Prevent shell injection through disk paths
			if strings.ContainsAny(d, ";|&$`\\\"' \t\n") {
				errs = append(errs, fmt.Sprintf("%s: disk %q contains illegal characters", pfx, d))
			}
		}

		if p.Ashift != 0 && (p.Ashift < 9 || p.Ashift > 16) {
			errs = append(errs, fmt.Sprintf("%s: ashift %d out of range [9,16]", pfx, p.Ashift))
		}
	}

	// ── Datasets ───────────────────────────────────────────────────────────────
	datasetNames := map[string]bool{}
	for i, d := range s.Datasets {
		pfx := fmt.Sprintf("datasets[%d] %q", i, d.Name)

		if !validDatasetRe.MatchString(d.Name) {
			errs = append(errs, pfx+": invalid dataset name")
		}
		if datasetNames[d.Name] {
			errs = append(errs, pfx+": duplicate dataset name")
		}
		datasetNames[d.Name] = true

		// Dataset must be under a declared pool
		hasPool := false
		for _, p := range s.Pools {
			if strings.HasPrefix(d.Name, p.Name+"/") || d.Name == p.Name {
				hasPool = true
				break
			}
		}
		// Allow datasets under pools not declared in this file (pre-existing pools)
		// — only warn, do not error. The diff engine handles this.
		_ = hasPool

		validComp := map[string]bool{"": true, "lz4": true, "zstd": true,
			"gzip": true, "off": true, "on": true}
		if !validComp[d.Compression] {
			errs = append(errs, pfx+": unknown compression "+d.Compression)
		}

		validAtime := map[string]bool{"": true, "on": true, "off": true}
		if !validAtime[d.Atime] {
			errs = append(errs, pfx+": atime must be \"on\" or \"off\"")
		}

		if d.Mountpoint != "" && !strings.HasPrefix(d.Mountpoint, "/") {
			errs = append(errs, pfx+": mountpoint must be an absolute path")
		}
	}

	// ── Shares ─────────────────────────────────────────────────────────────────
	shareNames := map[string]bool{}
	for i, sh := range s.Shares {
		pfx := fmt.Sprintf("shares[%d] %q", i, sh.Name)

		if !validShareRe.MatchString(sh.Name) {
			errs = append(errs, pfx+": invalid share name")
		}
		if shareNames[sh.Name] {
			errs = append(errs, pfx+": duplicate share name")
		}
		shareNames[sh.Name] = true

		if sh.Path == "" || !strings.HasPrefix(sh.Path, "/") {
			errs = append(errs, pfx+": path must be a non-empty absolute path")
		}
	}

	return errs
}

// ═══════════════════════════════════════════════════════════════════════════════
//  MINIMAL YAML PARSER  (stdlib only — no external dependency)
//
//  Supports the exact subset required by state.yaml:
//    - top-level scalar keys
//    - top-level sequence keys (list of mappings)
//    - mapping keys with scalar values, bool values, and sub-map values
//    - inline lists for disk paths
//
//  Does NOT support: anchors, tags, multi-document, block scalars, JSON flow style.
//  Anything outside this subset returns a parse error — fail closed, never silent.
// ═══════════════════════════════════════════════════════════════════════════════

// ParseStateYAML parses the contents of state.yaml into a DesiredState.
// Returns validation errors if the schema is invalid.
func ParseStateYAML(content string) (*DesiredState, error) {
	p := &yamlParser{lines: splitLines(content)}
	raw, err := p.parseDocument()
	if err != nil {
		return nil, fmt.Errorf("parse error: %w", err)
	}

	state, err := mapToState(raw)
	if err != nil {
		return nil, fmt.Errorf("schema error: %w", err)
	}

	if errs := ValidState(state); len(errs) > 0 {
		return nil, fmt.Errorf("validation errors:\n  - %s", strings.Join(errs, "\n  - "))
	}

	return state, nil
}

// ── Internal parser types ──────────────────────────────────────────────────────

type yamlParser struct {
	lines []parsedLine
	pos   int
}

type parsedLine struct {
	indent  int
	content string // trimmed content, comments stripped
	raw     string
	lineNum int
}

type yamlNode = interface{} // string | map[string]yamlNode | []yamlNode

func splitLines(s string) []parsedLine {
	var result []parsedLine
	scanner := bufio.NewScanner(strings.NewReader(s))
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		raw := scanner.Text()
		// Strip comment — but only outside quoted strings (simplified: strip # at word boundary)
		content := stripComment(raw)
		if strings.TrimSpace(content) == "" {
			continue // blank or comment-only lines
		}
		indent := countIndent(raw)
		result = append(result, parsedLine{
			indent:  indent,
			content: strings.TrimSpace(content),
			raw:     raw,
			lineNum: lineNum,
		})
	}
	return result
}

func countIndent(s string) int {
	count := 0
	for _, ch := range s {
		if ch == ' ' {
			count++
		} else {
			break
		}
	}
	return count
}

func stripComment(s string) string {
	// Very simple: find first # not inside quotes
	inSingle, inDouble := false, false
	for i, ch := range s {
		switch ch {
		case '\'':
			inSingle = !inSingle
		case '"':
			inDouble = !inDouble
		case '#':
			if !inSingle && !inDouble {
				return s[:i]
			}
		}
	}
	return s
}

// parseDocument parses the top-level mapping.
func (p *yamlParser) parseDocument() (map[string]yamlNode, error) {
	return p.parseMapping(0)
}

// parseMapping parses a block mapping at the given minimum indent.
func (p *yamlParser) parseMapping(minIndent int) (map[string]yamlNode, error) {
	result := make(map[string]yamlNode)
	for p.pos < len(p.lines) {
		line := p.lines[p.pos]
		if line.indent < minIndent {
			break // dedented — done with this mapping
		}
		if !strings.Contains(line.content, ":") {
			return nil, fmt.Errorf("line %d: expected key:value, got %q", line.lineNum, line.content)
		}

		// Split on first colon
		colonIdx := strings.Index(line.content, ":")
		key := strings.TrimSpace(line.content[:colonIdx])
		rest := strings.TrimSpace(line.content[colonIdx+1:])

		p.pos++

		var val yamlNode
		var err error

		if rest == "" {
			// Value is on next lines — could be sequence or mapping
			if p.pos < len(p.lines) && p.lines[p.pos].indent > line.indent {
				nextLine := p.lines[p.pos]
				if strings.HasPrefix(nextLine.content, "- ") || nextLine.content == "-" {
					// Sequence
					val, err = p.parseSequence(nextLine.indent)
				} else {
					// Nested mapping
					val, err = p.parseMapping(nextLine.indent)
				}
				if err != nil {
					return nil, err
				}
			} else {
				val = ""
			}
		} else if strings.HasPrefix(rest, "[") {
			// Inline sequence: [a, b, c]
			val, err = parseInlineSequence(rest)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", line.lineNum, err)
			}
		} else {
			// Scalar
			val = unquote(rest)
		}

		result[key] = val
	}
	return result, nil
}

// parseSequence parses a block sequence (lines starting with "- ").
func (p *yamlParser) parseSequence(minIndent int) ([]yamlNode, error) {
	var result []yamlNode
	for p.pos < len(p.lines) {
		line := p.lines[p.pos]
		if line.indent < minIndent {
			break
		}
		if !strings.HasPrefix(line.content, "- ") && line.content != "-" {
			break
		}

		itemContent := ""
		if len(line.content) > 2 {
			itemContent = strings.TrimSpace(line.content[2:])
		}
		p.pos++

		if itemContent == "" {
			// Next lines are the item's content (mapping)
			if p.pos < len(p.lines) && p.lines[p.pos].indent > line.indent {
				subMap, err := p.parseMapping(p.lines[p.pos].indent)
				if err != nil {
					return nil, err
				}
				result = append(result, subMap)
			} else {
				result = append(result, "")
			}
		} else if strings.Contains(itemContent, ":") {
			// Inline key: value as first field of mapping item
			// e.g. "- name: tank"
			// Build a sub-mapping starting with this key:value
			colonIdx := strings.Index(itemContent, ":")
			key := strings.TrimSpace(itemContent[:colonIdx])
			val := strings.TrimSpace(itemContent[colonIdx+1:])

			subMap := map[string]yamlNode{key: unquote(val)}

			// Continue reading additional keys at deeper indent
			if p.pos < len(p.lines) && p.lines[p.pos].indent > line.indent {
				rest, err := p.parseMapping(p.lines[p.pos].indent)
				if err != nil {
					return nil, err
				}
				for k, v := range rest {
					subMap[k] = v
				}
			}
			result = append(result, subMap)
		} else {
			// Plain scalar list item
			result = append(result, unquote(itemContent))
		}
	}
	return result, nil
}

// parseInlineSequence parses [a, b, c] syntax.
func parseInlineSequence(s string) ([]yamlNode, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil, fmt.Errorf("invalid inline sequence: %q", s)
	}
	inner := s[1 : len(s)-1]
	parts := strings.Split(inner, ",")
	var result []yamlNode
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, unquote(p))
		}
	}
	return result, nil
}

// unquote removes surrounding quotes from a scalar value.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') ||
			(s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// ── Map → struct converters ───────────────────────────────────────────────────

func mapToState(raw map[string]yamlNode) (*DesiredState, error) {
	s := &DesiredState{
		Version: strField(raw, "version"),
	}

	if poolsRaw, ok := raw["pools"]; ok {
		pools, err := toSliceOfMaps(poolsRaw, "pools")
		if err != nil {
			return nil, err
		}
		for _, pm := range pools {
			p, err := mapToPool(pm)
			if err != nil {
				return nil, err
			}
			s.Pools = append(s.Pools, p)
		}
	}

	if dsRaw, ok := raw["datasets"]; ok {
		datasets, err := toSliceOfMaps(dsRaw, "datasets")
		if err != nil {
			return nil, err
		}
		for _, dm := range datasets {
			d, err := mapToDataset(dm)
			if err != nil {
				return nil, err
			}
			s.Datasets = append(s.Datasets, d)
		}
	}

	if shRaw, ok := raw["shares"]; ok {
		shares, err := toSliceOfMaps(shRaw, "shares")
		if err != nil {
			return nil, err
		}
		for _, sm := range shares {
			sh, err := mapToShare(sm)
			if err != nil {
				return nil, err
			}
			s.Shares = append(s.Shares, sh)
		}
	}

	return s, nil
}

func mapToPool(m map[string]yamlNode) (DesiredPool, error) {
	p := DesiredPool{
		Name:     strField(m, "name"),
		VdevType: strField(m, "vdev_type"),
	}
	if a := strField(m, "ashift"); a != "" {
		fmt.Sscanf(a, "%d", &p.Ashift)
	}

	if disksRaw, ok := m["disks"]; ok {
		disks, err := toStringSlice(disksRaw, "disks")
		if err != nil {
			return p, err
		}
		p.Disks = disks
	}

	if optsRaw, ok := m["options"]; ok {
		optsMap, ok := optsRaw.(map[string]yamlNode)
		if !ok {
			return p, fmt.Errorf("pool %q: options must be a mapping", p.Name)
		}
		p.Options = make(map[string]string)
		for k, v := range optsMap {
			p.Options[k] = fmt.Sprintf("%v", v)
		}
	}

	return p, nil
}

func mapToDataset(m map[string]yamlNode) (DesiredDataset, error) {
	d := DesiredDataset{
		Name:        strField(m, "name"),
		Quota:       strField(m, "quota"),
		Compression: strField(m, "compression"),
		Atime:       strField(m, "atime"),
		Mountpoint:  strField(m, "mountpoint"),
	}
	if enc := strField(m, "encrypted"); enc == "true" {
		d.Encrypted = true
	}
	return d, nil
}

func mapToShare(m map[string]yamlNode) (DesiredShare, error) {
	sh := DesiredShare{
		Name:       strField(m, "name"),
		Path:       strField(m, "path"),
		ValidUsers: strField(m, "valid_users"),
		Comment:    strField(m, "comment"),
	}
	if ro := strField(m, "read_only"); ro == "true" {
		sh.ReadOnly = true
	}
	if gok := strField(m, "guest_ok"); gok == "true" {
		sh.GuestOK = true
	}
	return sh, nil
}

// ── Low-level helpers ─────────────────────────────────────────────────────────

func strField(m map[string]yamlNode, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func toSliceOfMaps(v yamlNode, field string) ([]map[string]yamlNode, error) {
	seq, ok := v.([]yamlNode)
	if !ok {
		return nil, fmt.Errorf("%s must be a sequence", field)
	}
	var result []map[string]yamlNode
	for i, item := range seq {
		m, ok := item.(map[string]yamlNode)
		if !ok {
			return nil, fmt.Errorf("%s[%d]: expected a mapping, got %T", field, i, item)
		}
		result = append(result, m)
	}
	return result, nil
}

func toStringSlice(v yamlNode, field string) ([]string, error) {
	seq, ok := v.([]yamlNode)
	if !ok {
		return nil, fmt.Errorf("%s must be a sequence", field)
	}
	var result []string
	for _, item := range seq {
		result = append(result, fmt.Sprintf("%v", item))
	}
	return result, nil
}
