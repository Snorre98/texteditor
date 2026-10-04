package locate

import (
	"math"
	"strings"
	"unicode"
)

// fuzzyScore is the conservative similarity of two normalized strings: the
// larger of a token multiset Dice coefficient and a character-trigram Dice
// coefficient. Both are in [0,1]; the caller applies the threshold. No
// embeddings are involved (ADR-0048 §2).
func fuzzyScore(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	return math.Max(tokenDice(a, b), trigramDice(a, b))
}

// tokenDice is the Sørensen–Dice coefficient over lowercased word multisets.
func tokenDice(a, b string) float64 {
	ca := tokenCounts(a)
	cb := tokenCounts(b)
	if len(ca) == 0 || len(cb) == 0 {
		return 0
	}
	inter := 0
	total := 0
	for w, n := range ca {
		total += n
		if m, ok := cb[w]; ok {
			inter += min(n, m)
		}
	}
	for _, n := range cb {
		total += n
	}
	if total == 0 {
		return 0
	}
	return 2 * float64(inter) / float64(total)
}

// trigramDice is the Sørensen–Dice coefficient over character trigrams of the
// normalized strings. Strings shorter than three runes contribute one gram.
func trigramDice(a, b string) float64 {
	ga := trigrams(a)
	gb := trigrams(b)
	if len(ga) == 0 || len(gb) == 0 {
		return 0
	}
	setB := map[string]int{}
	for _, g := range gb {
		setB[g]++
	}
	inter := 0
	for _, g := range ga {
		if setB[g] > 0 {
			inter++
			setB[g]--
		}
	}
	return 2 * float64(inter) / float64(len(ga)+len(gb))
}

// tokenCounts splits a normalized string into lowercased alphanumeric words.
func tokenCounts(s string) map[string]int {
	out := map[string]int{}
	for _, w := range strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		out[strings.ToLower(w)]++
	}
	return out
}

// trigrams returns the sliding three-rune grams of s (whole string when shorter
// than three runes). Non-alphanumerics are kept so word boundaries stay visible.
func trigrams(s string) []string {
	r := []rune(s)
	if len(r) == 0 {
		return nil
	}
	if len(r) <= 3 {
		return []string{string(r)}
	}
	out := make([]string, 0, len(r)-2)
	for i := 0; i+3 <= len(r); i++ {
		out = append(out, string(r[i:i+3]))
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
