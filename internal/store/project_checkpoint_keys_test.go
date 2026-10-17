package store

import (
	"fmt"
	"strings"
	"testing"
)

func TestLegacyWhitespaceCheckpointKeysRespectConversationScope(t *testing.T) {
	f, db := newFactStore(t)
	for i, space := range []string{" ", "\t", "\r\n", "\u00a0", "\u3000"} {
		canonical := fmt.Sprintf("note/context-legacy%d", i)
		created, err := f.UpsertProjectFact(&ProjectFact{ProjectID: "p1", FactKey: canonical, SourceConversationID: "other", Summary: "LEGACY_PROGRESS"})
		if err != nil {
			t.Fatal(err)
		}
		oldKey := space + canonical + space
		// Simulate an already-stored legacy row, without migrating it on read.
		if _, err := db.Exec("UPDATE project_facts SET fact_key=? WHERE id=?", oldKey, created.ID); err != nil {
			t.Fatal(err)
		}
		if !IsContextCheckpointKey(oldKey) {
			t.Fatal("helper did not identify legacy key")
		}
		if err := ValidateFactKey(oldKey); err == nil {
			t.Fatal("write validation accepts noncanonical key")
		}
		if _, err := f.UpsertProjectFact(&ProjectFact{ProjectID: "p1", FactKey: space + "finding/new"}); err == nil {
			t.Fatal("write boundary stored noncanonical key")
		}
	}
	if _, err := f.UpsertProjectFact(&ProjectFact{ProjectID: "p1", FactKey: "finding/shared", Summary: "shared"}); err != nil {
		t.Fatal(err)
	}
	for _, filter := range []ProjectFactListFilter{{RestrictContextCheckpoints: true, ContextConversationID: "current"}, {RestrictContextCheckpoints: true, ContextConversationID: "current", Search: "LEGACY_PROGRESS"}, {RestrictContextCheckpoints: true}} {
		facts, err := f.ListProjectFacts("p1", filter, 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, fact := range facts {
			if IsContextCheckpointKey(fact.FactKey) {
				t.Fatalf("foreign legacy note crossed scope: %q", fact.FactKey)
			}
		}
	}
	index, err := f.ListProjectFactsForIndex("p1", false, "current")
	if err != nil || len(index) != 1 || index[0].FactKey != "finding/shared" {
		t.Fatalf("legacy note leaked into index: %#v %v", index, err)
	}
	history, err := f.ListProjectFacts("p1", ProjectFactListFilter{Search: "LEGACY_PROGRESS"}, 100, 0)
	if err != nil || len(history) != 5 {
		t.Fatalf("historical records lost: %#v %v", history, err)
	}
	for _, fact := range history {
		if fact.FactKey == strings.TrimSpace(fact.FactKey) {
			t.Fatal("read mutated legacy evidence")
		}
	}
	own, err := f.ListProjectFactsForIndex("p1", false, "other")
	if err != nil || len(own) != 6 {
		t.Fatalf("legacy origin no longer visible to owner: %#v %v", own, err)
	}
}
