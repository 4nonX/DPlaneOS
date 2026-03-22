package database

import (
	"fmt"
	"strings"
)

// Placeholders returns "$1,$2,...,$n" for use in PostgreSQL IN clauses
// and bulk insert statements.
func Placeholders(n int) string {
	if n == 0 {
		return ""
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("$%d", i+1)
	}
	return strings.Join(parts, ",")
}

// PlaceholderOffset returns "$offset+1,$offset+2,...,$offset+n"
// for use when building multi-row inserts with an existing parameter offset.
func PlaceholderOffset(n, offset int) string {
	if n == 0 {
		return ""
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("$%d", offset+i+1)
	}
	return strings.Join(parts, ",")
}
