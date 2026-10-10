package market

import (
	"math"
	"strconv"
	"strings"
	"unicode"
)

// Match az OCR sorait a helyi katalógushoz párosítja. Több sorra tört teljes
// tárgynevet is felismer, de részszöveget vagy bizonytalan rövidítést nem fogad el.
func Match(lines []string, items []Item) (Item, bool) {
	candidates := scanCandidates(lines)
	if len(candidates) == 0 {
		return Item{}, false
	}
	if item, count := exactMatches(candidates, items, false); count != 0 {
		return item, count == 1
	}
	// A teljes név egy erős OCR-találata előnyt kap az esetleges rövid nevekkel
	// szemben. Fuzzy keresés kizárólag hosszú teljes nevekre történik.
	if item, ok := fuzzyMatch(candidates, items); ok {
		return item, true
	}
	item, count := exactMatches(candidates, items, true)
	return item, count == 1
}

func normalizeName(value string) string {
	var normalized strings.Builder
	space := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && normalized.Len() > 0 {
				normalized.WriteByte(' ')
			}
			normalized.WriteRune(unicode.ToLower(r))
			space = false
		} else {
			space = true
		}
	}
	return normalized.String()
}

func compactName(value string) []rune {
	return []rune(strings.ReplaceAll(value, " ", ""))
}

func scanCandidates(lines []string) []string {
	// Egy képkivágásban csak kevés sor várható. A korlát megakadályozza, hogy
	// véletlenül teljes képernyőnyi szöveget vagy rendkívül hosszú sort dolgozzunk fel.
	if len(lines) > 80 {
		lines = lines[:80]
	}
	seen := make(map[string]bool)
	var candidates []string
	for start := range lines {
		var joined string
		for end := start; end < len(lines) && end < start+3; end++ {
			line := strings.TrimSpace(lines[end])
			if line == "" || len([]rune(line)) > 200 {
				break
			}
			if joined != "" {
				joined += " "
			}
			joined += line
			candidate := normalizeName(joined)
			length := len(compactName(candidate))
			if length >= 4 && length <= 200 && !seen[candidate] {
				seen[candidate] = true
				candidates = append(candidates, candidate)
			}
		}
	}
	return candidates
}

func exactMatches(candidates []string, items []Item, short bool) (Item, int) {
	texts := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		texts[candidate] = true
	}
	matched := make(map[string]Item)
	for index, item := range items {
		name := item.Name
		if short {
			name = item.ShortName
		}
		normalized := normalizeName(name)
		if len(compactName(normalized)) >= 4 && texts[normalized] {
			matched[itemIdentity(item, index)] = item
		}
	}
	if len(matched) == 1 {
		for _, item := range matched {
			return item, 1
		}
	}
	return Item{}, len(matched)
}

func itemIdentity(item Item, index int) string {
	if item.UID != "" {
		return item.UID
	}
	// Katalóguson kívüli tesztadatokban sem vonunk össze eltérő tárgyakat.
	return strconv.Itoa(index) + ":" + item.Name
}

func fuzzyMatch(candidates []string, items []Item) (Item, bool) {
	type scoredItem struct {
		item  Item
		score float64
	}
	scores := make(map[string]scoredItem)
	var texts [][]rune
	for _, candidate := range candidates {
		text := compactName(candidate)
		if len(text) >= 12 && len(text) <= 120 {
			texts = append(texts, text)
		}
	}
	for index, item := range items {
		name := compactName(normalizeName(item.Name))
		if len(name) < 12 || len(name) > 120 {
			continue
		}
		for _, text := range texts {
			if len(text)-len(name) > 3 || len(name)-len(text) > 3 || !sameDigits(text, name) {
				continue
			}
			length := max(len(text), len(name))
			// A legfeljebb két OCR-hiba mellett a második legközelebbi
			// találatot is figyelembe vesszük az egyértelműséghez.
			distance := editDistance(text, name, 3)
			if distance > 3 {
				continue
			}
			score := float64(distance) / float64(length)
			identity := itemIdentity(item, index)
			previous, exists := scores[identity]
			if !exists || score < previous.score {
				scores[identity] = scoredItem{item: item, score: score}
			}
		}
	}
	best, runnerUp := math.Inf(1), math.Inf(1)
	var winner Item
	for _, match := range scores {
		if match.score < best {
			runnerUp, best, winner = best, match.score, match.item
		} else if match.score < runnerUp {
			runnerUp = match.score
		}
	}
	if best > 0.06 || runnerUp-best < 0.04 {
		return Item{}, false
	}
	// Hosszú névben is legfeljebb két eltérő karakter fogadható el.
	for _, candidate := range candidates {
		text := compactName(candidate)
		name := compactName(normalizeName(winner.Name))
		if len(text) >= 12 && sameDigits(text, name) && editDistance(text, name, 2) <= 2 &&
			float64(editDistance(text, name, 2))/float64(max(len(text), len(name))) <= 0.06 {
			return winner, true
		}
	}
	return Item{}, false
}

// A számok nem javíthatók automatikusan: AK-74 és AK-72 közötti különbség
// például tárgyazonosságot is jelenthet, nem feltétlenül OCR-hibát.
func sameDigits(a, b []rune) bool {
	var left, right strings.Builder
	for _, r := range a {
		if unicode.IsDigit(r) {
			left.WriteRune(r)
		}
	}
	for _, r := range b {
		if unicode.IsDigit(r) {
			right.WriteRune(r)
		}
	}
	return left.String() == right.String()
}

func editDistance(a, b []rune, limit int) int {
	if len(a)-len(b) > limit || len(b)-len(a) > limit {
		return limit + 1
	}
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current[0] = i
		rowMin := i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
			rowMin = min(rowMin, current[j])
		}
		if rowMin > limit {
			return limit + 1
		}
		previous, current = current, previous
	}
	return previous[len(b)]
}
