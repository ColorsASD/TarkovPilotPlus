package market

import (
	"math"
	"testing"
	"unicode/utf16"
)

func TestMatchInspectionRequiresTitleAndNamedCatalogItem(t *testing.T) {
	for _, test := range []struct {
		name  string
		lines []string
		items []Item
	}{
		{"no lines", nil, []Item{{UID: "one", Name: "Item"}}},
		{"blank title", []string{" \t", "\r "}, []Item{{UID: "one", Name: "Item"}}},
		{"no catalog", []string{"Item"}, nil},
		{"unnamed items", []string{"Item"}, []Item{{UID: "one"}, {UID: "two", Name: "\t "}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			item, confidence, ok := MatchInspection(test.lines, test.items)
			if ok || item.UID != "" || confidence != 0 {
				t.Fatalf("empty input returned %#v, %v, %v", item, confidence, ok)
			}
		})
	}
}

func TestMatchInspectionRanksFullTitlesWithoutManualSearchLimits(t *testing.T) {
	items := []Item{
		{UID: "helmet", Name: "Galvion Caiman Hybrid helmet (MultiCam Alpine)", ShortName: "Caiman"},
		{UID: "black", Name: "Galvion Caiman Hybrid helmet (Black)", ShortName: "Caiman"},
		{UID: "flash", Name: "Secure Flash drive", ShortName: "Flash"},
		{UID: "gas", Name: "Gas analyzer", ShortName: "Gas"},
		{UID: "soap", Name: "Soap"},
		{UID: "50s", Name: "50s"},
		{UID: "ai2", Name: "AI-2 medikit", ShortName: "AI-2"},
	}
	for _, test := range []struct {
		name  string
		lines []string
		uid   string
	}{
		{"several OCR mistakes", []string{"Galvion Caiman Hvbrid heimet (MultlCam AIpine)"}, "helmet"},
		{"missing characters", []string{"Galvion Caimn Hybrd helmet (MultiCam Alpne)"}, "helmet"},
		{"split title", []string{"Galvion Caiman Hybrid", "helmet (MultiCam Alpine)"}, "helmet"},
		{"quoted title", []string{"\"Galvion Caiman Hybrid helmet (MultiCam Alpine)\""}, "helmet"},
		{"partial title", []string{"Secure Flash"}, "flash"},
		{"short title OCR error", []string{"50s?"}, "50s"},
		{"outside whitespace", []string{"\tSoap \r "}, "soap"},
	} {
		t.Run(test.name, func(t *testing.T) {
			item, confidence, ok := MatchInspection(test.lines, items)
			if !ok || item.UID != test.uid || confidence < 0 || confidence > 1 {
				t.Fatalf("got %#v, %v, %v; want %s", item, confidence, ok, test.uid)
			}
		})
	}
}

func TestMatchInspectionRanksFullNamesInsteadOfShortNames(t *testing.T) {
	items := []Item{
		{UID: "analyzer", Name: "Gas analyzer", ShortName: "Gas"},
		{UID: "closer", Name: "Gasx"},
	}
	item, confidence, ok := MatchInspection([]string{"Gas"}, items)
	if !ok || item.UID != "closer" || confidence != 0.75 {
		t.Fatalf("short-name index overrode closer full name: %#v, %v, %v", item, confidence, ok)
	}
}

func TestMatchInspectionDoesNotRejectChangedNumbersOrLowScores(t *testing.T) {
	for _, test := range []struct {
		text, name string
		want       float64
	}{
		{"AK-72N", "AK-74N", 5.0 / 6},
		{"5", "5", 1},
		{"ABC", "XYZ", 0},
		{"AI-2", "AI-2 medikit", 4.0 / 12},
	} {
		item, confidence, ok := MatchInspection([]string{test.text}, []Item{{UID: "one", Name: test.name}})
		if !ok || item.UID != "one" || math.Abs(confidence-test.want) > 1e-12 {
			t.Errorf("%q → %q: got %#v, %v, %v; want %v", test.text, test.name, item, confidence, ok, test.want)
		}
	}
	items := []Item{{UID: "74", Name: "AK-74N"}, {UID: "72", Name: "AK-72N"}}
	if item, _, ok := MatchInspection([]string{"AK-72N"}, items); !ok || item.UID != "72" {
		t.Fatalf("exact numeric alternative was not preferred: %#v, %v", item, ok)
	}
}

func TestMatchInspectionLaterCatalogItemWinsTies(t *testing.T) {
	items := []Item{{UID: "a", Name: "ABC"}, {UID: "b", Name: "ABD"}, {UID: "blank"}}
	item, confidence, ok := MatchInspection([]string{"ABX"}, items)
	if !ok || item.UID != "b" || math.Abs(confidence-2.0/3) > 1e-12 {
		t.Fatalf("tie did not choose later named item: %#v, %v, %v", item, confidence, ok)
	}
	items = []Item{{UID: "a", Name: "Soap"}, {UID: "b", Name: "Soap"}}
	item, confidence, ok = MatchInspection([]string{"Soap"}, items)
	if !ok || item.UID != "b" || confidence != 1 {
		t.Fatalf("identical names did not choose later item: %#v, %v, %v", item, confidence, ok)
	}
}

func TestMatchInspectionHandlesUppercaseCyrillicLookalikes(t *testing.T) {
	for _, test := range []struct {
		text, name string
		want       float64
	}{
		{"АІ-2 medikit", "AI-2 medikit", 1},
		{"ABESIKMHOPCTX", "АВЕЅІКМНОРСТХ", 1},
		{"Soap", "soap", 0.75},
		{"а", "a", 0},
	} {
		item, confidence, ok := MatchInspection([]string{test.text}, []Item{{UID: "one", Name: test.name}})
		if !ok || item.UID != "one" || math.Abs(confidence-test.want) > 1e-12 {
			t.Errorf("lookalike %q → %q: got %#v, %v, %v; want %v", test.text, test.name, item, confidence, ok, test.want)
		}
	}
}

func TestInspectionSimilarityUsesRawUTF16Characters(t *testing.T) {
	for _, test := range []struct {
		a, b string
		want float64
	}{
		{"helmet", "helmet", 1},
		{"helmet", "heimet", 5.0 / 6},
		{"helmet", "helme", 5.0 / 6},
		{"helmet", "helmets", 6.0 / 7},
		{"ABC", "abc", 0},
		{"\"Soap\"", "Soap", 4.0 / 6},
		{"AK-74", "AK74", 4.0 / 5},
		{"Soap\nbar", "Soap bar", 7.0 / 8},
		{"Különleges", "Különleges", 1},
		{"Фильтр", "Фильтр", 1},
		{"A😀B", "AB", 0.5},
		{"", "", 1},
		{"", "Soap", 0},
	} {
		left, right := utf16.Encode([]rune(test.a)), utf16.Encode([]rune(test.b))
		for _, pair := range [][2][]uint16{{left, right}, {right, left}} {
			if got := inspectionSimilarity(pair[0], pair[1]); math.Abs(got-test.want) > 1e-12 {
				t.Errorf("similarity(%q,%q)=%v; want %v", test.a, test.b, got, test.want)
			}
		}
	}
}
