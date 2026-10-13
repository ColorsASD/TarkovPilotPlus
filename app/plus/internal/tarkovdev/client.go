package tarkovdev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	apiURL           = "https://json.tarkov.dev"
	cacheTTL         = time.Hour
	maxResponseBytes = 32 << 20
	cacheVersion     = 1
)

var (
	ErrUnsupportedMode = errors.New("A tarkov.dev nem támogatja ezt a játékmódot")
	ErrInvalidCatalog  = errors.New("A tarkov.dev követelménylistája érvénytelen")
	ErrResponseTooBig  = errors.New("A tarkov.dev válasza túl nagy")
)

type Client struct {
	cacheDir string
	baseURL  string
	http     *http.Client
	now      func() time.Time
	maxBytes int64
	mu       sync.Mutex
	catalogs map[string]Catalog
}

func NewClient(cacheDir string) *Client {
	return &Client{
		cacheDir: cacheDir,
		baseURL:  apiURL,
		http: &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		now:      time.Now,
		maxBytes: maxResponseBytes,
		catalogs: make(map[string]Catalog),
	}
}

// Load a helyi gyorsítótárat használja, majd szükség esetén csak GET kérésekkel
// frissít. Sikertelen frissítéskor a korábbi teljes katalógust Stale=true
// értékkel ÉS a hibával együtt adja vissza. Részleges lista nem írja felül.
func (c *Client) Load(ctx context.Context, mode string) (Catalog, error) {
	apiMode, err := normalizeMode(mode)
	if err != nil {
		return Catalog{}, err
	}
	if err := ctx.Err(); err != nil {
		return Catalog{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Catalog{}, err
	}
	cached, ok := c.catalogs[apiMode]
	if !ok {
		cached, ok = c.readCache(apiMode)
		if ok {
			c.catalogs[apiMode] = cached
		}
	}
	if ok {
		age := c.now().Sub(cached.UpdatedAt)
		if age >= 0 && age < cacheTTL {
			cached.Stale = false
			return cloneCatalog(cached), nil
		}
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := c.fetch(fetchCtx, apiMode)
	if err != nil {
		cached.Stale = true
		return cloneCatalog(cached), err
	}
	result.UpdatedAt = c.now()
	c.catalogs[apiMode] = result
	// A lemez íráshibája mellett a teljes, friss hálózati lista használható.
	_ = c.writeCache(apiMode, result)
	return cloneCatalog(result), nil
}

func normalizeMode(mode string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "PVP", "REGULAR":
		return "regular", nil
	case "PVE":
		return "pve", nil
	case "SEASON", "PVP-SEASON":
		return "pvp-season", nil
	default:
		return "", ErrUnsupportedMode
	}
}

type taskResponse struct {
	Data struct {
		Tasks map[string]rawTask `json:"tasks"`
	} `json:"data"`
}

type rawTask struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	NormalizedName string         `json:"normalizedName"`
	Objectives     []rawObjective `json:"objectives"`
}

type rawObjective struct {
	ID          string      `json:"id"`
	Type        string      `json:"type"`
	Items       []string    `json:"items"`
	Item        string      `json:"item"`
	MarkerItem  string      `json:"markerItem"`
	UseAny      []string    `json:"useAny"`
	Count       json.Number `json:"count"`
	FoundInRaid bool        `json:"foundInRaid"`
	Optional    bool        `json:"optional"`
}

type hideoutResponse struct {
	Data map[string]rawStation `json:"data"`
}

type rawStation struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	NormalizedName string `json:"normalizedName"`
	Levels         []struct {
		Level            int `json:"level"`
		ItemRequirements []struct {
			ID         string `json:"id"`
			Item       string `json:"item"`
			Count      int64  `json:"count"`
			Attributes struct {
				FoundInRaid bool `json:"foundInRaid"`
			} `json:"attributes"`
		} `json:"itemRequirements"`
	} `json:"levels"`
}

type translationResponse struct {
	Data map[string]string `json:"data"`
}

func (c *Client) fetch(ctx context.Context, mode string) (Catalog, error) {
	// A GraphQL régi GameMode enumja nem ismeri a szezont. A JSON API
	// explicit capability listája szükséges; PvP-adatra nincs visszaesés.
	if mode == "pvp-season" {
		var capabilities struct {
			Data struct {
				GameModes []string `json:"gameModes"`
			} `json:"data"`
		}
		if err := c.get(ctx, "/endpoints", &capabilities); err != nil {
			return Catalog{}, err
		}
		supported := false
		for _, gameMode := range capabilities.Data.GameModes {
			supported = supported || gameMode == mode
		}
		if !supported {
			return Catalog{}, ErrUnsupportedMode
		}
	}
	var tasks taskResponse
	var hideout hideoutResponse
	var taskNames, hideoutNames translationResponse
	for _, request := range []struct {
		path string
		dest any
	}{
		{"/" + mode + "/tasks", &tasks},
		{"/" + mode + "/hideout", &hideout},
		{"/" + mode + "/tasks_en", &taskNames},
		{"/" + mode + "/hideout_en", &hideoutNames},
	} {
		if err := c.get(ctx, request.path, request.dest); err != nil {
			return Catalog{}, err
		}
	}
	if len(tasks.Data.Tasks) == 0 || len(hideout.Data) == 0 {
		return Catalog{}, ErrInvalidCatalog
	}
	result := Catalog{}
	for key, raw := range tasks.Data.Tasks {
		if raw.ID == "" || raw.ID != key {
			return Catalog{}, ErrInvalidCatalog
		}
		task := Task{ID: raw.ID, Name: translatedName(raw.Name, raw.NormalizedName, taskNames.Data)}
		for _, rawObjective := range raw.Objectives {
			if rawObjective.ID == "" || rawObjective.Type == "" {
				return Catalog{}, ErrInvalidCatalog
			}
			objective := Objective{ID: rawObjective.ID, Type: rawObjective.Type,
				FoundInRaid: rawObjective.FoundInRaid, Optional: rawObjective.Optional}
			ids := append([]string(nil), rawObjective.Items...)
			if rawObjective.Item != "" {
				ids = append(ids, rawObjective.Item)
			}
			if rawObjective.MarkerItem != "" {
				ids = append(ids, rawObjective.MarkerItem)
			}
			ids = append(ids, rawObjective.UseAny...)
			seen := make(map[string]bool)
			for _, id := range ids {
				if strings.TrimSpace(id) == "" {
					return Catalog{}, ErrInvalidCatalog
				}
				if !seen[id] {
					objective.ItemIDs = append(objective.ItemIDs, id)
					seen[id] = true
				}
			}
			if rawObjective.Count != "" {
				count, err := rawObjective.Count.Int64()
				if err != nil || count < 0 {
					return Catalog{}, ErrInvalidCatalog
				}
				objective.Count = count
			}
			if len(objective.ItemIDs) > 0 && objective.Count <= 0 {
				return Catalog{}, ErrInvalidCatalog
			}
			task.Objectives = append(task.Objectives, objective)
		}
		result.Tasks = append(result.Tasks, task)
	}
	for key, raw := range hideout.Data {
		if raw.ID == "" || raw.ID != key {
			return Catalog{}, ErrInvalidCatalog
		}
		station := Station{ID: raw.ID, Name: translatedName(raw.Name, raw.NormalizedName, hideoutNames.Data), NormalizedName: raw.NormalizedName}
		for _, rawLevel := range raw.Levels {
			level := Level{Level: rawLevel.Level}
			for _, rawRequirement := range rawLevel.ItemRequirements {
				level.Requirements = append(level.Requirements, ItemRequirement{
					ID: rawRequirement.ID, ItemID: rawRequirement.Item, Count: rawRequirement.Count,
					FoundInRaid: rawRequirement.Attributes.FoundInRaid,
				})
			}
			station.Levels = append(station.Levels, level)
		}
		sort.Slice(station.Levels, func(i, j int) bool { return station.Levels[i].Level < station.Levels[j].Level })
		result.Hideout = append(result.Hideout, station)
	}
	if !validCatalog(result) {
		return Catalog{}, ErrInvalidCatalog
	}
	sort.Slice(result.Tasks, func(i, j int) bool { return result.Tasks[i].ID < result.Tasks[j].ID })
	sort.Slice(result.Hideout, func(i, j int) bool { return result.Hideout[i].ID < result.Hideout[j].ID })
	return result, nil
}

func translatedName(key, fallback string, names map[string]string) string {
	if translated := strings.TrimSpace(names[key]); translated != "" {
		return translated
	}
	if fallback != "" {
		return fallback
	}
	return key
}

func (c *Client) get(ctx context.Context, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("tarkov.dev kérés: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("tarkov.dev lekérés: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("tarkov.dev HTTP állapot: %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, c.maxBytes+1))
	if err != nil {
		return fmt.Errorf("tarkov.dev válasz: %w", err)
	}
	if int64(len(body)) > c.maxBytes {
		return ErrResponseTooBig
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("%w: JSON feldolgozás", ErrInvalidCatalog)
	}
	return nil
}

type diskCatalog struct {
	Version int     `json:"version"`
	Mode    string  `json:"mode"`
	Catalog Catalog `json:"catalog"`
}

func (c *Client) cachePath(mode string) string {
	return filepath.Join(c.cacheDir, "tarkovdev-requirements-"+mode+"-en.json")
}

func (c *Client) readCache(mode string) (Catalog, bool) {
	if c.cacheDir == "" {
		return Catalog{}, false
	}
	file, err := os.Open(c.cachePath(mode))
	if err != nil {
		return Catalog{}, false
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return Catalog{}, false
	}
	var cached diskCatalog
	if json.Unmarshal(body, &cached) != nil || cached.Version != cacheVersion || cached.Mode != mode ||
		cached.Catalog.UpdatedAt.IsZero() || !validCatalog(cached.Catalog) {
		return Catalog{}, false
	}
	return cached.Catalog, true
}

func (c *Client) writeCache(mode string, catalog Catalog) error {
	if c.cacheDir == "" {
		return nil
	}
	if err := os.MkdirAll(c.cacheDir, 0700); err != nil {
		return err
	}
	body, err := json.Marshal(diskCatalog{Version: cacheVersion, Mode: mode, Catalog: catalog})
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(c.cacheDir, ".tarkovdev-requirements-*.tmp")
	if err != nil {
		return err
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(path, c.cachePath(mode))
}

func validCatalog(catalog Catalog) bool {
	if len(catalog.Tasks) == 0 || len(catalog.Hideout) == 0 {
		return false
	}
	taskIDs, stationIDs := make(map[string]bool), make(map[string]bool)
	for _, task := range catalog.Tasks {
		if task.ID == "" || task.Name == "" || taskIDs[task.ID] {
			return false
		}
		taskIDs[task.ID] = true
		objectiveIDs := make(map[string]bool)
		for _, objective := range task.Objectives {
			if objective.ID == "" || objective.Type == "" || objectiveIDs[objective.ID] || objective.Count < 0 {
				return false
			}
			objectiveIDs[objective.ID] = true
			if len(objective.ItemIDs) > 0 && objective.Count <= 0 {
				return false
			}
			itemIDs := make(map[string]bool)
			for _, id := range objective.ItemIDs {
				if strings.TrimSpace(id) == "" || itemIDs[id] {
					return false
				}
				itemIDs[id] = true
			}
		}
	}
	for _, station := range catalog.Hideout {
		if station.ID == "" || station.Name == "" || stationIDs[station.ID] || len(station.Levels) == 0 {
			return false
		}
		stationIDs[station.ID] = true
		levels := make(map[int]bool)
		for _, level := range station.Levels {
			if level.Level < 1 || levels[level.Level] {
				return false
			}
			levels[level.Level] = true
			requirementIDs := make(map[string]bool)
			for _, requirement := range level.Requirements {
				if requirement.ID == "" || strings.TrimSpace(requirement.ItemID) == "" ||
					requirement.Count <= 0 || requirementIDs[requirement.ID] {
					return false
				}
				requirementIDs[requirement.ID] = true
			}
		}
	}
	return true
}

func cloneCatalog(catalog Catalog) Catalog {
	catalog.Tasks = append([]Task(nil), catalog.Tasks...)
	for i := range catalog.Tasks {
		catalog.Tasks[i].Objectives = append([]Objective(nil), catalog.Tasks[i].Objectives...)
		for j := range catalog.Tasks[i].Objectives {
			objective := &catalog.Tasks[i].Objectives[j]
			objective.ItemIDs = append([]string(nil), objective.ItemIDs...)
		}
	}
	catalog.Hideout = append([]Station(nil), catalog.Hideout...)
	for i := range catalog.Hideout {
		catalog.Hideout[i].Levels = append([]Level(nil), catalog.Hideout[i].Levels...)
		for j := range catalog.Hideout[i].Levels {
			level := &catalog.Hideout[i].Levels[j]
			level.Requirements = append([]ItemRequirement(nil), level.Requirements...)
		}
	}
	return catalog
}
