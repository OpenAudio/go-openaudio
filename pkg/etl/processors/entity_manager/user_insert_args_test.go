package entity_manager

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// insertedUserValue returns the value bound to a users column by the INSERT in
// user_create.go, looked up by column name.
//
// Tests used to assert on argument offsets ("the three state flags are the
// trailing arguments"), which broke every time a column was added -- and the
// breakage looked like a logic failure rather than a moved index. Pairing the
// column list with the VALUES tokens keeps the assertions readable and immune
// to reordering.
func insertedUserValue(t *testing.T, args []any, column string) any {
	t.Helper()
	src, err := os.ReadFile("user_create.go")
	if err != nil {
		t.Fatalf("read user_create.go: %v", err)
	}
	m := regexp.MustCompile(`(?s)INSERT INTO users \((.*?)\)\s*VALUES \((.*?)\)`).FindStringSubmatch(string(src))
	if m == nil {
		t.Fatal("could not locate the users INSERT -- it was reshaped; update this helper")
	}
	cols := splitSQLList(m[1])
	vals := splitSQLList(m[2])
	if len(cols) != len(vals) {
		t.Fatalf("INSERT has %d columns but %d values", len(cols), len(vals))
	}
	for i, c := range cols {
		if c != column {
			continue
		}
		v := vals[i]
		if !strings.HasPrefix(v, "$") {
			return v // a literal such as true/false
		}
		n, err := strconv.Atoi(v[1:])
		if err != nil || n < 1 || n > len(args) {
			t.Fatalf("column %q binds %s but only %d args were passed", column, v, len(args))
		}
		return args[n-1]
	}
	t.Fatalf("column %q is not in the users INSERT", column)
	return nil
}

func splitSQLList(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		for _, tok := range strings.Split(line, ",") {
			if tok = strings.TrimSpace(tok); tok != "" {
				out = append(out, tok)
			}
		}
	}
	return out
}
