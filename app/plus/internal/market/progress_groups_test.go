package market

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func groupSnapshotFixture() TrackedSnapshot {
	return TrackedSnapshot{CharUID: "char-1", GameMode: "SEASON", Revision: "group-revision", Groups: []TrackedGroup{{QuestID: "first-in-line", QuestName: "First in Line", RequirementID: "medkits", Name: "Medkits", Amount: 3, FIR: true, ItemIDs: []string{"salewa", "cms"}, RequiredQuantity: 3, AvailableQuantity: 1, RemainingQuantity: 2}}, Items: []TrackedItem{{ItemID: "salewa", AvailableQuantity: 1, SpentQuantity: 4, Sources: []TrackedSource{{Type: "questGroup", QuestID: "first-in-line", RequirementID: "medkits", Name: "First in Line", GroupName: "Medkits", Amount: 3, FIR: true}}}}}
}

func TestTrackedMarketGroupsAndRequirementIdentityArePreserved(t *testing.T) {
	want := groupSnapshotFixture()
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	client := progressTestClient(t, func(*http.Request) (*http.Response, error) { return response(200, string(body)), nil })
	got, err := client.LoadTracked(context.Background(), "fixture-key", "char-1", "en")
	if err != nil || len(got.Groups) != 1 || len(got.Items) != 1 {
		t.Fatalf("Market groups were discarded: %+v %v", got, err)
	}
	group, source := got.Groups[0], got.Items[0].Sources[0]
	if group.RequirementID != "medkits" || group.QuestName != "First in Line" || group.Name != "Medkits" || group.RequiredQuantity != 3 || group.AvailableQuantity != 1 || group.RemainingQuantity != 2 || len(group.ItemIDs) != 2 || !group.FIR || source.Type != "questGroup" || source.RequirementID != "medkits" || source.GroupName != "Medkits" || source.Name != "First in Line" || source.RemainingRequiredQuantity != 0 || got.Items[0].SpentQuantity != 4 {
		t.Fatalf("shared requirement or raw counters changed: %+v", got)
	}
}

func TestTrackedMarketGroupsAllowMissingQuestIDWithDistinctRequirements(t *testing.T) {
	for _, questField := range []string{`"questId":null,`, `"questId":"",`, ``} {
		t.Run(questField, func(t *testing.T) {
			want := groupSnapshotFixture()
			for _, requirement := range []struct {
				id     string
				amount int64
			}{{"event-medkits-5", 5}, {"event-medkits-7", 7}} {
				group := want.Groups[0]
				group.QuestID, group.QuestName = "", "Halloween: Tightening the Quarantine"
				group.RequirementID, group.Amount = requirement.id, requirement.amount
				group.RequiredQuantity, group.RemainingQuantity = requirement.amount, requirement.amount-1
				want.Groups = append(want.Groups, group)
				source := want.Items[0].Sources[0]
				source.QuestID, source.Name = "", group.QuestName
				source.RequirementID, source.Amount = group.RequirementID, group.Amount
				want.Items[0].Sources = append(want.Items[0].Sources, source)
			}
			body, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			payload := strings.ReplaceAll(string(body), `"questId":"",`, questField)
			client := progressTestClient(t, func(*http.Request) (*http.Response, error) {
				return response(200, payload), nil
			})
			got, err := client.LoadTracked(context.Background(), "fixture-key", "char-1", "en")
			if err != nil {
				t.Fatalf("event group without a quest ID blocked inventory: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("group identity, sources or stock changed: got %+v, want %+v", got, want)
			}
		})
	}
}

func TestInvalidMarketGroupsAreRejectedBeforeProjection(t *testing.T) {
	for _, name := range []string{"duplicate-group", "duplicate-event-group", "duplicate-member", "empty-members", "negative", "required-over-amount", "invalid-id", "invalid-quest-id", "event-without-requirement-id"} {
		t.Run(name, func(t *testing.T) {
			snapshot := groupSnapshotFixture()
			switch name {
			case "duplicate-group":
				snapshot.Groups = append(snapshot.Groups, snapshot.Groups[0])
			case "duplicate-event-group":
				snapshot.Groups[0].QuestID = ""
				snapshot.Groups = append(snapshot.Groups, snapshot.Groups[0])
			case "duplicate-member":
				snapshot.Groups[0].ItemIDs = []string{"salewa", "salewa"}
			case "empty-members":
				snapshot.Groups[0].ItemIDs = nil
			case "negative":
				snapshot.Groups[0].RemainingQuantity = -1
			case "required-over-amount":
				snapshot.Groups[0].RequiredQuantity = 4
			case "invalid-id":
				snapshot.Groups[0].RequirementID = "invalid/requirement"
			case "invalid-quest-id":
				snapshot.Groups[0].QuestID = "invalid/quest"
			case "event-without-requirement-id":
				snapshot.Groups[0].QuestID, snapshot.Groups[0].RequirementID = "", ""
			}
			body, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			client := progressTestClient(t, func(*http.Request) (*http.Response, error) { return response(200, string(body)), nil })
			if _, err := client.LoadTracked(context.Background(), "fixture-key", "char-1", "en"); !errors.Is(err, ErrInvalidProgress) {
				t.Fatalf("invalid group was accepted: %v", err)
			}
		})
	}
}
