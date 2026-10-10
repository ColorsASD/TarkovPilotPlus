package market

import "testing"

func TestMatchFullNameAcrossLinesAndNormalizedPunctuation(t *testing.T) {
	items := []Item{
		{UID: "helmet", Name: "Galvion Caiman Hybrid helmet (MultiCam Alpine)", ShortName: "Caiman"},
		{UID: "ledx", Name: "LEDX Skin Transilluminator", ShortName: "LEDX"},
	}
	for _, lines := range [][]string{
		{"GALVION CAIMAN HYBRID", "helmet [MultiCam Alpine]"},
		{"Galvion Caiman Hybrid helmet (MultiCam Alpine)", "25 660 ₽"},
		{"Galvion   Caiman Hybrid", "helmet", "MultiCam Alpine"},
	} {
		item, ok := Match(lines, items)
		if !ok || item.UID != "helmet" {
			t.Fatalf("did not recognize split full name %v: %#v, %v", lines, item, ok)
		}
	}
}

func TestMatchFullNameHasPriorityOverShortName(t *testing.T) {
	items := []Item{
		{UID: "helmet", Name: "Galvion Caiman Hybrid helmet (MultiCam Alpine)", ShortName: "Caiman"},
		{UID: "ledx", Name: "LEDX Skin Transilluminator", ShortName: "LEDX"},
	}
	item, ok := Match([]string{"LEDX", "Galvion Caiman Hybrid helmet (MultiCam Alpine)"}, items)
	if !ok || item.UID != "helmet" {
		t.Fatalf("short name overrode a full name: %#v, %v", item, ok)
	}
}

func TestMatchShortNamesNeedUniqueAndSufficientlyLongNames(t *testing.T) {
	items := []Item{
		{UID: "ledx", Name: "LEDX Skin Transilluminator", ShortName: "LEDX"},
		{UID: "ai2", Name: "AI-2 medikit", ShortName: "AI-2"},
		{UID: "helmet-a", Name: "Galvion Caiman Hybrid helmet (MultiCam Alpine)", ShortName: "Caiman"},
		{UID: "helmet-b", Name: "Galvion Caiman Hybrid helmet (Black)", ShortName: "Caiman"},
	}
	if item, ok := Match([]string{"LEDX"}, items); !ok || item.UID != "ledx" {
		t.Fatal("unique four-character short name was rejected")
	}
	for _, lines := range [][]string{{"AI-2"}, {"Caiman"}, {"L E"}, {"Inspect LEDX"}, {"Price LEDX 200 ₽"}, {"random application text"}, {}} {
		if item, ok := Match(lines, items); ok {
			t.Errorf("unsafe or ambiguous match for %v: %#v", lines, item)
		}
	}
}

func TestMatchRejectsMultipleFullNamesAndDuplicateNames(t *testing.T) {
	items := []Item{
		{UID: "ledx", Name: "LEDX Skin Transilluminator", ShortName: "LEDX"},
		{UID: "flash", Name: "Secure Flash drive", ShortName: "Flash drive"},
	}
	if _, ok := Match([]string{"LEDX Skin Transilluminator", "Secure Flash drive"}, items); ok {
		t.Fatal("screen containing two complete names was treated as unambiguous")
	}
	items = append(items, Item{UID: "different-ledx", Name: "LEDX Skin Transilluminator"})
	if _, ok := Match([]string{"LEDX Skin Transilluminator"}, items); ok {
		t.Fatal("duplicate full name was treated as unambiguous")
	}
}

func TestMatchAllowsOnlyStrongUnambiguousOCRCorrection(t *testing.T) {
	items := []Item{
		{UID: "helmet", Name: "Galvion Caiman Hybrid helmet (MultiCam Alpine)", ShortName: "Caiman"},
		{UID: "ledx", Name: "LEDX Skin Transilluminator", ShortName: "LEDX"},
	}
	for _, text := range []string{
		"Galvion Caiman Hybrid heimet (MultiCam Alpine)",
		"Galvion Caiman Hybrid heimet (MultlCam Alpine)",
	} {
		item, ok := Match([]string{text}, items)
		if !ok || item.UID != "helmet" {
			t.Fatalf("strong OCR correction rejected %q: %#v, %v", text, item, ok)
		}
	}
	for _, text := range []string{"Galvion Ca1man Hybrid helmet (MultiCam Alpine)", "Galvion Caiman Horrible helmet (MultiCam Alpine)", "LFDX", "flash drivo"} {
		if item, ok := Match([]string{text}, items); ok {
			t.Errorf("weak or numeric OCR correction accepted %q: %#v", text, item)
		}
	}
}

func TestMatchRejectsCloseFuzzyAlternativesAndChangedNumbers(t *testing.T) {
	items := []Item{
		{UID: "a", Name: "Example tactical equipment Alpha"},
		{UID: "b", Name: "Example tactical equipment Alpho"},
		{UID: "weapon", Name: "AK-74N Kalashnikov assault rifle"},
	}
	for _, text := range []string{"Example tactical equipment Alphx", "AK-72N Kalashnikov assault rifle"} {
		if item, ok := Match([]string{text}, items); ok {
			t.Errorf("ambiguous or numerically distinct match accepted %q: %#v", text, item)
		}
	}
}

func TestMatchSupportsUnicodeNames(t *testing.T) {
	items := []Item{{UID: "hu", Name: "Különleges egészségügyi felszerelés", ShortName: "Felszerelés"}}
	item, ok := Match([]string{"KÜLÖNLEGES EGÉSZSÉGÜGYI", "FELSZERELÉS"}, items)
	if !ok || item.UID != "hu" {
		t.Fatalf("Unicode full name rejected: %#v, %v", item, ok)
	}
}

func TestEditDistanceBounds(t *testing.T) {
	for _, test := range []struct {
		a, b string
		want int
	}{
		{"helmet", "helmet", 0},
		{"helmet", "heimet", 1},
		{"helmet", "helme", 1},
		{"helmet", "helmets", 1},
		{"helmet", "very different", 3},
		{"helmet", "xyzzy", 3},
	} {
		if got := editDistance([]rune(test.a), []rune(test.b), 2); got != test.want {
			t.Errorf("distance(%q,%q)=%d, want %d", test.a, test.b, got, test.want)
		}
	}
}
