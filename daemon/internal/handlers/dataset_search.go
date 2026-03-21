package handlers

import (
	"net/http"
	"strings"
)

// datasetSearchResult extends the basic dataset info with search highlighting.
type datasetSearchResult struct {
	Name            string `json:"name"`
	NameHighlighted string `json:"name_highlighted"`
	Used            string `json:"used"`
	Avail           string `json:"avail"`
	Refer           string `json:"refer"`
	Mountpoint      string `json:"mountpoint"`
	Type            string `json:"type"`
}

// HandleDatasetSearch serves GET /api/zfs/datasets/search?q=<query>&pool=<optional>
//
// Searches dataset names, mountpoints, and types. Supports:
//   - Plain substring match (case-insensitive)
//   - `pool:name` prefix to filter by pool
//   - `@` prefix or `/` in query to also match snapshot names
//
// Returns filtered datasets with a `name_highlighted` field where the
// matched portion is wrapped in <em>...</em> tags.
func HandleDatasetSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	pool := strings.TrimSpace(r.URL.Query().Get("pool"))

	// pool: prefix in query overrides the pool param
	if strings.HasPrefix(q, "pool:") {
		pool = strings.TrimPrefix(q, "pool:")
		q = ""
	}

	// Decide whether to also search snapshots
	includeSnapshots := strings.HasPrefix(q, "@") || strings.Contains(q, "/")
	snapQuery := strings.TrimPrefix(q, "@")

	// Fetch datasets: name,used,avail,refer,mountpoint,type
	dsOut, err := executeCommandWithTimeout(TimeoutFast, "zfs",
		[]string{"list", "-H", "-o", "name,used,avail,refer,mountpoint,type", "-r"})
	if err != nil {
		// On an empty system or error, return empty results rather than 500
		respondOK(w, map[string]interface{}{
			"success":  true,
			"results":  []datasetSearchResult{},
			"total":    0,
			"filtered": 0,
			"query":    q,
		})
		return
	}

	var all []datasetSearchResult
	for _, line := range strings.Split(strings.TrimSpace(dsOut), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 6)
		if len(parts) < 6 {
			continue
		}
		all = append(all, datasetSearchResult{
			Name:       strings.TrimSpace(parts[0]),
			Used:       strings.TrimSpace(parts[1]),
			Avail:      strings.TrimSpace(parts[2]),
			Refer:      strings.TrimSpace(parts[3]),
			Mountpoint: strings.TrimSpace(parts[4]),
			Type:       strings.TrimSpace(parts[5]),
		})
	}

	// Optionally fetch snapshots too
	if includeSnapshots {
		snapOut, snapErr := executeCommandWithTimeout(TimeoutFast, "zfs",
			[]string{"list", "-H", "-t", "snapshot", "-o", "name,used,avail,refer,mountpoint,type", "-r"})
		if snapErr == nil {
			for _, line := range strings.Split(strings.TrimSpace(snapOut), "\n") {
				if line == "" {
					continue
				}
				parts := strings.SplitN(line, "\t", 6)
				if len(parts) < 6 {
					continue
				}
				all = append(all, datasetSearchResult{
					Name:       strings.TrimSpace(parts[0]),
					Used:       strings.TrimSpace(parts[1]),
					Avail:      strings.TrimSpace(parts[2]),
					Refer:      strings.TrimSpace(parts[3]),
					Mountpoint: strings.TrimSpace(parts[4]),
					Type:       "snapshot",
				})
			}
		}
	}

	totalCount := len(all)

	// Apply filters
	qLower := strings.ToLower(q)
	snapQueryLower := strings.ToLower(snapQuery)
	poolLower := strings.ToLower(pool)

	var results []datasetSearchResult
	for _, d := range all {
		nameLower := strings.ToLower(d.Name)
		mntLower := strings.ToLower(d.Mountpoint)

		// Pool filter
		if poolLower != "" {
			if !strings.HasPrefix(nameLower, poolLower+"/") && nameLower != poolLower {
				continue
			}
		}

		// Query filter
		if qLower != "" {
			effectiveQ := qLower
			if d.Type == "snapshot" && includeSnapshots {
				effectiveQ = snapQueryLower
			}
			if effectiveQ != "" {
				matchName := strings.Contains(nameLower, effectiveQ)
				matchMnt := strings.Contains(mntLower, effectiveQ)
				if !matchName && !matchMnt {
					continue
				}
			}
		}

		// Build highlighted name
		if qLower != "" {
			effectiveQ := qLower
			if d.Type == "snapshot" && includeSnapshots {
				effectiveQ = snapQueryLower
			}
			d.NameHighlighted = highlightSubstring(d.Name, effectiveQ)
		} else {
			d.NameHighlighted = d.Name
		}

		results = append(results, d)
	}

	if results == nil {
		results = []datasetSearchResult{}
	}

	respondOK(w, map[string]interface{}{
		"success":  true,
		"results":  results,
		"total":    totalCount,
		"filtered": len(results),
		"query":    q,
		"pool":     pool,
	})
}

// highlightSubstring wraps the first occurrence of needle (case-insensitive)
// in haystack with <em>...</em> tags, preserving original case in the output.
func highlightSubstring(haystack, needle string) string {
	if needle == "" {
		return haystack
	}
	lower := strings.ToLower(haystack)
	idx := strings.Index(lower, strings.ToLower(needle))
	if idx < 0 {
		return haystack
	}
	return haystack[:idx] + "<em>" + haystack[idx:idx+len(needle)] + "</em>" + haystack[idx+len(needle):]
}
