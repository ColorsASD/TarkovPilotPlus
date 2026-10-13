package market

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Quest struct {
	QuestID        string   `json:"questId"`
	QuestIDAliases []string `json:"questIdAliases"`
	Name           string   `json:"name"`
	Trader         string   `json:"trader"`
	Completed      bool     `json:"completed"`
	Status         string   `json:"status"`
}

type QuestSnapshot struct {
	CharUID  string  `json:"charUid"`
	GameMode string  `json:"gameMode"`
	Revision string  `json:"revision"`
	Quests   []Quest `json:"quests"`
}

type HideoutUpgrade struct {
	UpgradeKey  string `json:"upgradeKey"`
	ModuleID    string `json:"moduleId"`
	Name        string `json:"name"`
	Level       int    `json:"level"`
	Completed   bool   `json:"completed"`
	Prebuilt    bool   `json:"prebuilt"`
	CanComplete bool   `json:"canComplete"`
}

type HideoutSnapshot struct {
	CharUID  string           `json:"charUid"`
	GameMode string           `json:"gameMode"`
	Revision string           `json:"revision"`
	Modules  []HideoutUpgrade `json:"modules"`
}

// LoadQuests a karakterhez tartozó küldetéseket és teljesítéseket olvassa.
// A személyes pillanatkép nem kerül gyorsítótárba; a módot a karakter határozza meg.
func (c *Client) LoadQuests(ctx context.Context, apiKey, charUID, lang string) (QuestSnapshot, error) {
	if !validProgressID(charUID) {
		return QuestSnapshot{}, ErrProgressInvalidRequest
	}
	_, lang, err := parameters("pvp", lang)
	if err != nil {
		return QuestSnapshot{}, err
	}
	body, err := c.progressRequest(ctx, apiKey, http.MethodGet, "/characters/"+charUID+"/quests", url.Values{"lang": {lang}}, nil, "")
	if err != nil {
		return QuestSnapshot{}, err
	}
	var snapshot QuestSnapshot
	if json.Unmarshal(body, &snapshot) != nil || snapshot.CharUID != charUID || !validGameMode(snapshot.GameMode) ||
		!validRevision(snapshot.Revision) || snapshot.Quests == nil {
		return QuestSnapshot{}, ErrInvalidProgress
	}
	owners := make(map[string]int)
	for i, quest := range snapshot.Quests {
		if !validProgressID(quest.QuestID) {
			return QuestSnapshot{}, ErrInvalidProgress
		}
		if _, duplicate := owners[quest.QuestID]; duplicate {
			return QuestSnapshot{}, ErrInvalidProgress
		}
		owners[quest.QuestID] = i
	}
	// Az aliasok ugyanazt a küldetést jelölik. Másik küldetés azonosítójával
	// ütköző aliasból nem lehet egyértelmű teljesítési állapotot meghatározni.
	for i, quest := range snapshot.Quests {
		for _, alias := range quest.QuestIDAliases {
			if !validProgressID(alias) {
				return QuestSnapshot{}, ErrInvalidProgress
			}
			if owner, exists := owners[alias]; exists && owner != i {
				return QuestSnapshot{}, ErrInvalidProgress
			}
			owners[alias] = i
		}
	}
	return snapshot, nil
}

// LoadHideout minden fejlesztési szint állapotát külön őrzi meg. A dokumentált
// null moduleId üres ModuleID marad; névből vagy upgradeKey-ből nem találunk ki BSG-ID-t.
func (c *Client) LoadHideout(ctx context.Context, apiKey, charUID, lang string) (HideoutSnapshot, error) {
	if !validProgressID(charUID) {
		return HideoutSnapshot{}, ErrProgressInvalidRequest
	}
	_, lang, err := parameters("pvp", lang)
	if err != nil {
		return HideoutSnapshot{}, err
	}
	body, err := c.progressRequest(ctx, apiKey, http.MethodGet, "/characters/"+charUID+"/hideout", url.Values{"lang": {lang}}, nil, "")
	if err != nil {
		return HideoutSnapshot{}, err
	}
	var snapshot HideoutSnapshot
	if !utf8.Valid(body) || json.Unmarshal(body, &snapshot) != nil || snapshot.CharUID != charUID || !validGameMode(snapshot.GameMode) ||
		!validRevision(snapshot.Revision) || snapshot.Modules == nil {
		return HideoutSnapshot{}, ErrInvalidProgress
	}
	seen := make(map[string]bool)
	for _, upgrade := range snapshot.Modules {
		if !validUpgradeKey(upgrade.UpgradeKey) || seen[upgrade.UpgradeKey] || upgrade.Level < 1 ||
			(upgrade.ModuleID != "" && !validProgressID(upgrade.ModuleID)) {
			return HideoutSnapshot{}, ErrInvalidProgress
		}
		seen[upgrade.UpgradeKey] = true
	}
	return snapshot, nil
}

// A fejlesztési kulcs a Market saját, szóközt és Unicode-karaktereket is
// tartalmazó neve. Az olvasás nem használja URL-útvonalban, ezért nem kezeljük
// BSG-azonosítóként; csak az üres, túl hosszú vagy vezérlőkarakteres érték hibás.
func validUpgradeKey(value string) bool {
	if len(value) == 0 || len(value) > 128 || !utf8.ValidString(value) || strings.TrimSpace(value) == "" {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp) {
			return false
		}
	}
	return true
}
