package handlers

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"dplaned/internal/reconciler"
)

var (
	sqlStart    = regexp.MustCompile(`(?is)^\s*(SELECT|INSERT\s+INTO|UPDATE|DELETE\s+FROM|WITH)\s`)
	sqlKeyword  = regexp.MustCompile(`(?i)\b(FROM|INTO|SET)\b`)
	sqlFormat   = regexp.MustCompile(`%[svdq]`)
	sqlMethods  = map[string]bool{"Exec": true, "Query": true, "QueryRow": true, "ExecContext": true, "QueryContext": true, "QueryRowContext": true}
	sqlParamRef = regexp.MustCompile(`\$(\d+)`)
)

// Every SQL statement written out in the daemon is prepared against the
// migrated schema: a statement that names a table or column that does not
// exist, or compares types that do not compare, fails here and not on the
// one code path nobody ran (sign-in of directory users did, for releases).
//
// Where the statement is passed straight to Exec/Query/QueryRow, the Go
// values are checked against the parameter types the database expects: a
// bool for an integer column (or the reverse) is refused by the driver at
// run time.
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

	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
		Dir: "../..",
		Env: append(os.Environ(), "GOOS=linux", "CGO_ENABLED=0"),
	}, "./internal/...", "./cmd/...")
	if err != nil {
		t.Fatal(err)
	}

	type stmt struct {
		where string
		sql   string
		args  []types.Type // nil: not passed straight to a query method
	}
	var stmts []stmt
	literal := func(p *packages.Package, e ast.Expr) (string, bool) {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil || !sqlStart.MatchString(s) || !sqlKeyword.MatchString(s) || sqlFormat.MatchString(s) {
			return "", false
		}
		return s, true
	}
	for _, p := range pkgs {
		for _, f := range p.Syntax {
			inCall, partOfConcat := map[ast.Expr]bool{}, map[ast.Expr]bool{}
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.BinaryExpr:
					if x.Op == token.ADD {
						partOfConcat[x.X], partOfConcat[x.Y] = true, true
					}
				case *ast.CallExpr:
					sel, ok := x.Fun.(*ast.SelectorExpr)
					if !ok || !sqlMethods[sel.Sel.Name] {
						return true
					}
					at := 0
					if strings.HasSuffix(sel.Sel.Name, "Context") {
						at = 1
					}
					if len(x.Args) <= at {
						return true
					}
					s, ok := literal(p, x.Args[at])
					if !ok {
						return true
					}
					inCall[x.Args[at]] = true
					st := stmt{where: p.Fset.Position(x.Pos()).String(), sql: s, args: []types.Type{}}
					if x.Ellipsis.IsValid() {
						st.args = nil // args... : not known here
					} else {
						for _, a := range x.Args[at+1:] {
							st.args = append(st.args, p.TypesInfo.TypeOf(a))
						}
					}
					stmts = append(stmts, st)
				}
				return true
			})
			ast.Inspect(f, func(n ast.Node) bool {
				e, ok := n.(ast.Expr)
				if !ok || inCall[e] || partOfConcat[e] {
					return true
				}
				if s, ok := literal(p, e); ok {
					stmts = append(stmts, stmt{where: p.Fset.Position(e.Pos()).String(), sql: s})
				}
				return true
			})
		}
	}
	if len(stmts) < 400 {
		t.Fatalf("only %d statements found: the scan is broken", len(stmts))
	}

	var bad []string
	report := func(s stmt, format string, a ...any) {
		bad = append(bad, fmt.Sprintf("%s\n    %s\n    %s", s.where, fmt.Sprintf(format, a...), strings.Join(strings.Fields(s.sql), " ")))
	}
	checkedArgs := 0
	for i, s := range stmts {
		name := fmt.Sprintf("chk_%d", i)
		q := strings.TrimRight(strings.TrimSpace(s.sql), ";")
		if _, err := conn.ExecContext(ctx, "PREPARE "+name+" AS "+q); err != nil {
			report(s, "%v", err)
			continue
		}
		var paramTypes string
		if err := conn.QueryRowContext(ctx, `SELECT array_to_string(parameter_types::text[], ',') FROM pg_prepared_statements WHERE name = $1`, name).Scan(&paramTypes); err != nil {
			t.Fatal(err)
		}
		_, _ = conn.ExecContext(ctx, "DEALLOCATE "+name)
		if s.args == nil {
			continue
		}
		var params []string
		if paramTypes != "" {
			params = strings.Split(paramTypes, ",")
		}
		highest := 0
		for _, m := range sqlParamRef.FindAllStringSubmatch(q, -1) {
			if n, _ := strconv.Atoi(m[1]); n > highest {
				highest = n
			}
		}
		if len(s.args) != highest {
			report(s, "the statement has %d parameters, the call passes %d values", highest, len(s.args))
			continue
		}
		for n, goType := range s.args {
			if n >= len(params) || goType == nil {
				continue
			}
			basic, ok := goType.Underlying().(*types.Basic)
			if !ok {
				continue
			}
			checkedArgs++
			pg := params[n]
			isBool := basic.Info()&types.IsBoolean != 0
			isInt := basic.Info()&types.IsInteger != 0
			switch {
			case isBool && pg != "boolean" && pg != "text" && pg != "unknown":
				report(s, "value %d is a Go bool, the database expects %s", n+1, pg)
			case isInt && pg == "boolean":
				report(s, "value %d is a Go integer, the database expects boolean", n+1)
			}
		}
	}
	if checkedArgs < 500 {
		t.Fatalf("only %d values checked: the scan is broken", checkedArgs)
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("%d of %d SQL statements do not match the schema:\n%s", len(bad), len(stmts), strings.Join(bad, "\n"))
	}
}
