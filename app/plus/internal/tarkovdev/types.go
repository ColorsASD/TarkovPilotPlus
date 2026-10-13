// Package tarkovdev a nyilvános tarkov.dev API küldetés- és hideout-követelményeit
// olvassa. A karakter előrehaladása és a Market kulcsa nem kerül az API-hoz.
package tarkovdev

import "time"

type Catalog struct {
	Tasks     []Task    `json:"tasks"`
	Hideout   []Station `json:"hideout"`
	UpdatedAt time.Time `json:"updatedAt"`
	Stale     bool      `json:"stale"`
}

type Task struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Objectives []Objective `json:"objectives"`
}

// Az ItemIDs egy objektív elfogadható tárgyait tartalmazza. Count a teljes
// csoport darabszáma; nem szabad minden alternatívára külön összeadni.
type Objective struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	ItemIDs     []string `json:"itemIds"`
	Count       int64    `json:"count"`
	FoundInRaid bool     `json:"foundInRaid"`
	Optional    bool     `json:"optional"`
}

type Station struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	NormalizedName string  `json:"normalizedName"`
	Levels         []Level `json:"levels"`
}

type Level struct {
	Level        int               `json:"level"`
	Requirements []ItemRequirement `json:"requirements"`
}

type ItemRequirement struct {
	ID          string `json:"id"`
	ItemID      string `json:"itemId"`
	Count       int64  `json:"count"`
	FoundInRaid bool   `json:"foundInRaid"`
}
