package handlers

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"dplaned/internal/reconciler"
)

var sqlStart = regexp.MustCompile(`(?is)^\s*(SELECT|INSERT\s+INTO|UPDATE|DELETE\s+FROM|WITH)\s`)

// Every SQL statement written out in the daemon is prepared against the
// migrated schema: a statement that names a table or column that does not
// exist, or compares types that do not compare, fails here and not on the
// one code path nobody ran (sign-in of directory users did, for releases).
//
// Statements built at run time (concatenated, or with %s) are not checked.
func TestSQLStatementsMatchTheSchema(t *testing.T) {
	db := testDB(t)
	// Tables the daemon creates at start, outside the migrations.
	if err := reconciler.EnsureSchema(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	root := filepath.Join("..", "..")
	type stmt struct{ where, sql string }
	var stmts []stmt
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "vendor" || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		// Parts of a concatenation are not statements of their own.
		partOfConcat := map[ast.Node]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			if b, ok := n.(*ast.BinaryExpr); ok && b.Op == token.ADD {
				partOfConcat[b.X], partOfConcat[b.Y] = true, true
			}
			return true
		})
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || partOfConcat[n] {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil || !sqlStart.MatchString(s) {
				return true
			}
			if strings.Contains(s, "%") && regexp.MustCompile(`%[svdq]`).MatchString(s) {
				return true // a format string, completed at run time
			}
			if !regexp.MustCompile(`(?i)\b(FROM|INTO|SET)\b`).MatchString(s) {
				return true // prose that happens to start with "Select ..."
			}
			stmts = append(stmts, stmt{fset.Position(lit.Pos()).String(), s})
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) < 200 {
		t.Fatalf("only %d statements found: the scan is broken", len(stmts))
	}

	var bad []string
	for i, s := range stmts {
		name := fmt.Sprintf("chk_%d", i)
		q := strings.TrimRight(strings.TrimSpace(s.sql), ";")
		if strings.Contains(q, ";") && !strings.Contains(q, "'") {
			continue // several statements in one string: run with Exec, not preparable
		}
		if _, err := conn.ExecContext(ctx, "PREPARE "+name+" AS "+q); err != nil {
			msg := err.Error()
			// Tables of the session store live in the security package's
			// own schema setup and are covered by its tests.
			bad = append(bad, fmt.Sprintf("%s\n    %s\n    %s", s.where, msg, strings.Join(strings.Fields(q), " ")))
			continue
		}
		_, _ = conn.ExecContext(ctx, "DEALLOCATE "+name)
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("%d of %d SQL statements do not match the schema:\n%s", len(bad), len(stmts), strings.Join(bad, "\n"))
	}
}
