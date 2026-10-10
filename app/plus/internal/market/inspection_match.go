package market

import (
	"strings"
	"unicode/utf16"
)

// MatchInspection egyetlen felismert Inspect-ablak címéhez keresi a legközelebbi
// teljes katalógusnevet. A sorok ugyanannak a címnek a részei; külön ablakok vagy
// külön OCR-változatok sorait a hívó külön párosítja.
//
// A visszaadott hasonlóság 0 és 1 közötti érték. Nem OCR-bizonyosság: a függvény
// bármely nem üres címhez választ tárgyat, ezért kizárólag az Inspect-ablak
// jelenlétének ellenőrzése után használható. Azonos pontszámnál a katalógusban
// később szereplő tárgy nyer. A kézi keresés szigorúbb szabályait a Match kezeli.
func MatchInspection(lines []string, items []Item) (Item, float64, bool) {
	title := strings.TrimSpace(strings.Join(lines, "\n"))
	if title == "" {
		return Item{}, 0, false
	}
	text := inspectionCharacters(title)
	var winner Item
	confidence := 0.0
	found := false
	for _, item := range items {
		if strings.TrimSpace(item.Name) == "" {
			continue
		}
		name := inspectionCharacters(item.Name)
		score := inspectionSimilarity(text, name)
		if !found || score >= confidence {
			winner, confidence, found = item, score, true
		}
	}
	return winner, confidence, found
}

// inspectionCharacters az OCR során összekeverhető nagy cirill betűket a
// latin betűalakjukra hozza. A többi karakter és a kisbetűk változatlanok.
func inspectionCharacters(value string) []uint16 {
	characters := []rune(value)
	for i, r := range characters {
		switch r {
		case 'А':
			characters[i] = 'A'
		case 'В':
			characters[i] = 'B'
		case 'Е':
			characters[i] = 'E'
		case 'Ѕ':
			characters[i] = 'S'
		case 'І':
			characters[i] = 'I'
		case 'К':
			characters[i] = 'K'
		case 'М':
			characters[i] = 'M'
		case 'Н':
			characters[i] = 'H'
		case 'О':
			characters[i] = 'O'
		case 'Р':
			characters[i] = 'P'
		case 'С':
			characters[i] = 'C'
		case 'Т':
			characters[i] = 'T'
		case 'Х':
			characters[i] = 'X'
		}
	}
	return utf16.Encode(characters)
}

// inspectionSimilarity a teljes nyers cím és név szerkesztési távolságát méri.
// A karakterek UTF-16 egységek, így a kiegészítő Unicode-karakterek is ugyanúgy
// számítanak, mint a Windows szövegfeldolgozásában. A kis- és nagybetűk,
// írásjelek, szóközök és számok egyaránt részei az összehasonlításnak.
func inspectionSimilarity(a, b []uint16) float64 {
	length := max(len(a), len(b))
	if length == 0 {
		return 1
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	// Egyetlen sor elég: a felülírt bal felső cellát külön változó őrzi.
	row := make([]int, len(a)+1)
	for j := range row {
		row[j] = j
	}
	for i, right := range b {
		diagonal := row[0]
		row[0] = i + 1
		for j, left := range a {
			above := row[j+1]
			substitution := diagonal
			if left != right {
				substitution++
			}
			row[j+1] = min(above+1, row[j]+1, substitution)
			diagonal = above
		}
	}
	return float64(length-row[len(a)]) / float64(length)
}
