package search

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// FoldSQL is the SQL function (migration 000088) that folds a column the way FoldWord folds a search word. A trigram
// expression index over FoldSQL(column) serves the ILIKE patterns of SubstringPatterns.
const FoldSQL = "platform.fold_de"

const maxSubstringWords = 8

// FoldWord lower-cases a word and replaces German umlauts and the sharp s by their base letters, like FoldSQL.
func FoldWord(w string) string {
	return strings.NewReplacer("ä", "a", "ö", "o", "ü", "u", "ß", "ss").Replace(strings.ToLower(w))
}

// SubstringPatterns turns a query into ILIKE patterns (`%word%`, wildcards escaped with a backslash, to be used
// with ESCAPE '\') for records that must contain every word as a substring of a folded column. Words are split at
// spaces, hyphens and slashes ("Gast-WLAN" is two words), umlauts and the sharp s are folded ("Gast" finds
// "Gäste"), at most eight words count. Words shorter than a trigram are dropped as long as a longer word remains,
// because only a longer word can use the trigram index; a query made of short words only keeps them. An empty
// query yields no patterns.
func SubstringPatterns(text string) []string {
	words := strings.FieldsFunc(text, func(r rune) bool { return unicode.IsSpace(r) || r == '-' || r == '/' })
	var long, short []string
	for _, w := range words {
		p := "%" + likeEscape(FoldWord(w)) + "%"
		if utf8.RuneCountInString(w) >= 3 {
			long = append(long, p)
		} else {
			short = append(short, p)
		}
	}
	out := long
	if len(out) == 0 {
		out = short
	}
	if len(out) > maxSubstringWords {
		out = out[:maxSubstringWords]
	}
	return out
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
