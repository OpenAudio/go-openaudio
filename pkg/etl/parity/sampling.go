package main

import "fmt"

// sampleConfig controls how wide tables are subsampled for the row-by-row
// comparison.
//
// The row comparison costs one indexed lookup against the reference database
// per candidate row. follows alone has 26 million of them, so comparing every
// row is not a run anyone waits for. Sampling bounds that at a stated cost in
// coverage -- stated being the point: a silent cap reads as "covered
// everything", which is how a parity tool starts lying.
type sampleConfig struct {
	// Mod is the divisor. 1 compares every row; 0 picks a divisor per table
	// from the table's estimated size so each table yields about Target rows.
	Mod int
	// Offset is the residue to keep, in [0, Mod).
	Offset int
	// Target is the desired number of sampled rows per table when Mod is 0.
	Target int
}

const sampleModAuto = 0

// sampleDivisor resolves the divisor to use for a table.
//
// estRows < 0 means the size is unknown, in which case auto mode declines to
// sample rather than guessing: comparing too much is a slow run, comparing a
// mystery fraction is a bad report.
func sampleDivisor(estRows int64, cfg sampleConfig) int {
	if cfg.Mod > 0 {
		return cfg.Mod
	}
	if estRows < 0 || cfg.Target <= 0 {
		return 1
	}
	if estRows <= int64(cfg.Target) {
		return 1
	}
	n := (estRows + int64(cfg.Target) - 1) / int64(cfg.Target)
	if n < 1 {
		return 1
	}
	return int(n)
}

// samplePredicate builds the SQL filter selecting one residue class of a table.
//
// It has to hold two properties at once. It must be deterministic, so that a
// failing run can be re-run against the same rows, and it must select the same
// logical rows on both sides -- which is why this is arithmetic on the entity
// id and not TABLESAMPLE. TABLESAMPLE picks physical blocks; two databases
// built independently do not lay the same rows out in the same blocks, so the
// same seed would sample different entities on each side.
//
// abs() keeps the residue class well defined for negative ids (Postgres mod()
// returns a negative remainder for a negative argument), and the bigint cast
// keeps abs() from overflowing on the minimum int4.
func samplePredicate(col string, mod, offset int) string {
	if mod <= 1 {
		return ""
	}
	k := offset % mod
	if k < 0 {
		k += mod
	}
	return fmt.Sprintf("mod(abs(%s::bigint), %d) = %d", col, mod, k)
}

// sampleNote describes a resolved sample for the report. Callers print it
// verbatim so that every table line says how much of the table it actually
// looked at.
func sampleNote(col string, mod, offset int, estRows int64) string {
	if mod <= 1 {
		return "full scan (no sampling)"
	}
	note := fmt.Sprintf("sampled %s", samplePredicate(col, mod, offset))
	if estRows >= 0 {
		note += fmt.Sprintf(" (~1/%d of ~%d rows)", mod, estRows)
	} else {
		note += fmt.Sprintf(" (~1/%d of the table)", mod)
	}
	return note
}
