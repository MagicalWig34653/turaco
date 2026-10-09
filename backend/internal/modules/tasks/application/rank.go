package application

import (
	"errors"
	"strings"
)

// Fractional card ranks (F13 slice Q-D). A rank is a non-empty string over the base-62 alphabet below that never
// ends in the lowest digit, so there is always room before and after it. Ranks compare bytewise (collation "C"),
// which equals the alphabet order. rankBetween returns a rank strictly between two others without touching any
// other card; the lowest and highest sentinel are the empty string.

const rankAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

const (
	rankBase = len(rankAlphabet)
	// maxRankLength is where a Board's ranks are rebalanced (the database accepts 64).
	maxRankLength = 48
)

var errRankOrder = errors.New("tasks: rank bounds are not ordered")

func rankDigit(c byte) int { return strings.IndexByte(rankAlphabet, c) }

// validRank reports whether s is a well-formed rank.
func validRank(s string) bool {
	if s == "" || len(s) > 64 || s[len(s)-1] == rankAlphabet[0] {
		return false
	}
	for i := 0; i < len(s); i++ {
		if rankDigit(s[i]) < 0 {
			return false
		}
	}
	return true
}

// rankBetween returns a rank r with a < r < b. a == "" means no lower bound, b == "" no upper bound.
func rankBetween(a, b string) (string, error) {
	if (a != "" && !validRank(a)) || (b != "" && !validRank(b)) || (b != "" && a >= b) {
		return "", errRankOrder
	}
	return midpoint(a, b), nil
}

func midpoint(a, b string) string {
	if b != "" {
		// Skip the common prefix; a is treated as padded with the lowest digit.
		n := 0
		for n < len(b) {
			var ca byte = rankAlphabet[0]
			if n < len(a) {
				ca = a[n]
			}
			if ca != b[n] {
				break
			}
			n++
		}
		if n > 0 {
			rest := ""
			if n < len(a) {
				rest = a[n:]
			}
			return b[:n] + midpoint(rest, b[n:])
		}
	}
	digitA := 0
	if a != "" {
		digitA = rankDigit(a[0])
	}
	digitB := rankBase
	if b != "" {
		digitB = rankDigit(b[0])
	}
	if digitB-digitA > 1 {
		return string(rankAlphabet[(digitA+digitB)/2])
	}
	// The first digits are consecutive.
	if len(b) > 1 {
		return b[:1]
	}
	rest := ""
	if len(a) > 1 {
		rest = a[1:]
	}
	return string(rankAlphabet[digitA]) + midpoint(rest, "")
}

// spreadRank returns the rank of position i (0-based) among n evenly spread ranks of three digits.
func spreadRank(i, n int) string {
	const space = 62 * 62 * 62
	step := space / (n + 1)
	v := (i + 1) * step
	if v%rankBase == 0 {
		v++
	}
	out := []byte{rankAlphabet[v/(rankBase*rankBase)%rankBase], rankAlphabet[v/rankBase%rankBase], rankAlphabet[v%rankBase]}
	return string(out)
}

// SpreadRank is the rank of position i (0-based) among n evenly spread ranks; the store uses it to rebalance a Board.
func SpreadRank(i, n int) string { return spreadRank(i, n) }

// ranksAfter returns n ascending ranks that all sort after lo ("" = from the start), each lo plus a short counter
// suffix, so a run of cards is ranked in one step without growing the ranks one digit per card.
func ranksAfter(lo string, n int) []string {
	width := 1
	for capacity := rankBase - 1; capacity < n; capacity *= rankBase - 1 {
		width++
	}
	out := make([]string, 0, n)
	for c := 1; len(out) < n; c++ {
		if c%rankBase == 0 {
			continue // a rank never ends in the lowest digit
		}
		suffix := make([]byte, width)
		for i, v := width-1, c; i >= 0; i, v = i-1, v/rankBase {
			suffix[i] = rankAlphabet[v%rankBase]
		}
		out = append(out, lo+string(suffix))
	}
	return out
}
