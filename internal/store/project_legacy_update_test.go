package store

import (
	"testing"
)

func TestLegacyPaddedFactCanUpdateWithoutChangingIdentity(t *testing.T) {
	f, db := newFactStore(t)
	if _, err := db.Exec(`INSERT INTO projects (id, name) VALUES ('other', 'other')`); err != nil {
		t.Fatalf("seed other project: %v", err)
	}
	created, err := f.UpsertProjectFact(&ProjectFact{ProjectID: "p1", FactKey: "finding/legacy", Summary: "before", Body: "original evidence"})
	if err != nil {
		t.Fatal(err)
	}
	legacyKey := "\u3000 finding/legacy \t"
	if _, err := db.Exec("UPDATE project_facts SET fact_key=? WHERE id=?", legacyKey, created.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err := f.GetProjectFact(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Summary = "after"
	loaded.Body = ""
	updated, err := f.UpsertProjectFact(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != created.ID || updated.FactKey != legacyKey || updated.Summary != "after" || updated.Body != "original evidence" {
		t.Fatalf("legacy edit changed identity or evidence: %+v", updated)
	}
	stored, err := f.GetProjectFactByKey("p1", legacyKey)
	if err != nil || stored.ID != created.ID || stored.FactKey != legacyKey || stored.Summary != "after" {
		t.Fatalf("legacy key was rewritten: %+v %v", stored, err)
	}
	for name, candidate := range map[string]*ProjectFact{
		"new key":           {ProjectID: "p1", FactKey: " finding/new ", Summary: "new"},
		"existing key only": {ProjectID: "p1", FactKey: legacyKey, Summary: "overwrite"},
		"wrong record":      {ID: "not-the-stored-id", ProjectID: "p1", FactKey: legacyKey, Summary: "overwrite"},
		"wrong project":     {ID: created.ID, ProjectID: "other", FactKey: legacyKey, Summary: "overwrite"},
		"changed key":       {ID: created.ID, ProjectID: "p1", FactKey: " finding/new ", Summary: "overwrite"},
		"invalid core":      {ID: created.ID, ProjectID: "p1", FactKey: " finding/invalid? ", Summary: "overwrite"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.UpsertProjectFact(candidate); err == nil {
				t.Fatal("legacy exception accepted a new or mismatched key")
			}
		})
	}
	stored, err = f.GetProjectFact(created.ID)
	if err != nil || stored.FactKey != legacyKey || stored.Summary != "after" || stored.Body != "original evidence" {
		t.Fatalf("rejected write modified stored evidence: %+v %v", stored, err)
	}
	// Padding was accepted by the old validator, but arbitrary invalid keys
	// were not. A stored malformed core must not widen this compatibility path.
	malformed, err := f.UpsertProjectFact(&ProjectFact{ProjectID: "p1", FactKey: "finding/malformed", Summary: "untouched"})
	if err != nil {
		t.Fatal(err)
	}
	malformed.FactKey = " finding/invalid? "
	if _, err := db.Exec("UPDATE project_facts SET fact_key=? WHERE id=?", malformed.FactKey, malformed.ID); err != nil {
		t.Fatal(err)
	}
	malformed.Summary = "overwrite"
	if _, err := f.UpsertProjectFact(malformed); err == nil {
		t.Fatal("compatibility exception accepted an invalid trimmed key")
	}
}
