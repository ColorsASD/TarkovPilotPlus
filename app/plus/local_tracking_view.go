package plus

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
)

type localTrackingDisplayGroup struct {
	id, kind                 string
	fir, completed, optional bool
	count, amount, remaining int64
	allocated                int64
	alternatives             int
	members                  map[string]bool
	candidates               string
	valid                    bool
}

type localTrackingDisplayKey struct {
	candidates, kind string
	fir              bool
}

type localTrackingDisplayBucket struct {
	key                 localTrackingDisplayKey
	ids                 []string
	available, required int64
	open                bool
}

// simplifyLocalTrackingView csak a nézetet alakítja át. A fix követelmények
// külön maradnak a globális Any arányoktól; a csoportok belső azonossága és
// készletkiosztása nem változik a megjelenítéshez használt összevonástól.
func simplifyLocalTrackingView(view *ItemTrackingState, row localprogress.Item, snapshot localprogress.Snapshot) {
	if view == nil || len(view.Variants) != 0 {
		return
	}
	rows := make(map[string]int)
	for _, item := range snapshot.Items {
		rows[item.ItemID]++
	}
	if rows[row.ItemID] != 1 {
		return
	}
	groups := localTrackingDisplayGroups(snapshot, rows)
	firAvailable, regularAvailable := row.CollectionFIRAvailableQuantity, row.CollectionRegularAvailableQuantity
	var firRequired, regularRequired int64
	completed := []ItemTrackingCompletedSource{}
	buckets := make(map[localTrackingDisplayKey]*localTrackingDisplayBucket)
	seenActive, seenCompleted := make(map[string]bool), make(map[string]bool)
	for _, source := range row.Sources {
		if source.Canceled {
			continue
		}
		group := groups[source.GroupID]
		knownGroup := group != nil && group.valid
		if source.Completed {
			if knownGroup && group.completed {
				if !seenCompleted[group.id] {
					completed = append(completed, ItemTrackingCompletedSource{Type: group.kind, Quantity: group.amount, Optional: group.optional})
					seenCompleted[group.id] = true
				}
			} else {
				completed = append(completed, ItemTrackingCompletedSource{Type: completedSourceType(source.TrackedSource), Quantity: source.Amount, Optional: source.Optional})
			}
			continue
		}
		if source.Optional || source.RemainingRequiredQuantity <= 0 {
			continue
		}
		if knownGroup && !group.completed {
			if group.remaining > 0 {
				if source.FIR {
					firAvailable = max(0, firAvailable-source.AllocatedQuantity)
				} else {
					regularAvailable = max(0, regularAvailable-source.AllocatedQuantity)
				}
			}
			if !seenActive[group.id] {
				key := localTrackingDisplayKey{candidates: group.candidates, kind: group.kind, fir: group.fir}
				bucket := buckets[key]
				if bucket == nil {
					bucket = &localTrackingDisplayBucket{key: key}
					buckets[key] = bucket
				}
				bucket.ids = append(bucket.ids, group.id)
				bucket.required += group.count
				bucket.available += group.count - group.remaining
				bucket.open = bucket.open || group.remaining > 0
				seenActive[group.id] = true
			}
			continue
		}
		// A bizonytalan csoport az eredeti, kötelező követelményben marad.
		if source.FIR {
			firRequired += source.RemainingRequiredQuantity
		} else {
			regularRequired += source.RemainingRequiredQuantity
		}
	}
	view.Requirements = []ItemTrackingRequirement{}
	if firRequired > 0 {
		view.Requirements = append(view.Requirements, ItemTrackingRequirement{Kind: "fir", RequiredQuantity: firRequired, AvailableQuantity: min(firRequired, firAvailable)})
	}
	if regularRequired > 0 {
		view.Requirements = append(view.Requirements, ItemTrackingRequirement{Kind: "regular", RequiredQuantity: regularRequired, AvailableQuantity: min(regularRequired, regularAvailable)})
	}
	view.Requirement = "unknown"
	switch {
	case firRequired > 0 && regularRequired > 0:
		view.Requirement = "mixed"
	case firRequired > 0:
		view.Requirement = "fir"
	case regularRequired > 0:
		view.Requirement = "regular"
	}
	view.CompletedSources = completed
	view.AnyGroups = []ItemTrackingAnyGroup{}
	ordered := make([]*localTrackingDisplayBucket, 0, len(buckets))
	for _, bucket := range buckets {
		if bucket.open {
			ordered = append(ordered, bucket)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		left, right := ordered[i].key, ordered[j].key
		if left.fir != right.fir {
			return left.fir
		}
		if left.kind != right.kind {
			return left.kind < right.kind
		}
		return left.candidates < right.candidates
	})
	for _, bucket := range ordered {
		sort.Strings(bucket.ids)
		id := bucket.ids[0]
		if len(bucket.ids) > 1 {
			encoded, _ := json.Marshal(bucket.ids)
			id = "groups:" + string(encoded)
		}
		view.AnyGroups = append(view.AnyGroups, ItemTrackingAnyGroup{GroupID: id, FIR: bucket.key.fir, RequiredQuantity: bucket.required, AvailableQuantity: bucket.available})
	}
}

func localTrackingDisplayGroups(snapshot localprogress.Snapshot, rows map[string]int) map[string]*localTrackingDisplayGroup {
	groups := make(map[string]*localTrackingDisplayGroup)
	for _, item := range snapshot.Items {
		for _, source := range item.Sources {
			if source.GroupID == "" {
				continue
			}
			amount := source.GroupAmount
			if amount == 0 {
				amount = source.Amount
			}
			kind := completedSourceType(source.TrackedSource)
			group := groups[source.GroupID]
			if group == nil {
				group = &localTrackingDisplayGroup{id: source.GroupID, kind: kind, fir: source.FIR, completed: source.Completed, optional: source.Optional,
					count: source.RemainingRequiredQuantity, amount: amount, remaining: source.GroupRemainingQuantity,
					alternatives: source.Alternatives, members: make(map[string]bool), valid: true}
				groups[source.GroupID] = group
			}
			if rows[item.ItemID] != 1 || group.members[item.ItemID] || source.Alternatives <= 1 || source.Alternatives != group.alternatives || source.FIR != group.fir || source.Completed != group.completed || source.Optional != group.optional || source.Canceled || kind == "unknown" || kind != group.kind || amount <= 0 || amount > localprogress.MaxQuantity || amount != group.amount || source.RemainingRequiredQuantity != group.count {
				group.valid = false
			}
			if source.Completed {
				if source.RemainingRequiredQuantity != 0 {
					group.valid = false
				}
			} else if source.Optional || !source.AllocationKnown || source.RemainingRequiredQuantity <= 0 || source.GroupRemainingQuantity != group.remaining || source.GroupRemainingQuantity < 0 || source.GroupRemainingQuantity > source.RemainingRequiredQuantity || source.AllocatedQuantity < 0 || source.AllocatedQuantity > source.RemainingRequiredQuantity {
				group.valid = false
			}
			group.members[item.ItemID] = true
			if source.AllocatedQuantity > localprogress.MaxQuantity-group.allocated {
				group.valid = false
			} else {
				group.allocated += source.AllocatedQuantity
			}
		}
	}
	for _, group := range groups {
		if len(group.members) != group.alternatives || (!group.completed && group.allocated != group.count-group.remaining) {
			group.valid = false
		}
		ids := make([]string, 0, len(group.members))
		for id := range group.members {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		group.candidates = strings.Join(ids, "\x00")
	}
	return groups
}
