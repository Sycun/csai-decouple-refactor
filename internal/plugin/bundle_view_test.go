package plugin

import "testing"

// A bundle carries its own copy of its units from install time, so a switch that only updates the
// unit map leaves two views of one object disagreeing: the run path stops serving the unit while
// every bundle view still says it is enabled. That is what a real click in the console showed -
// the unit left the served set but its row stayed "enabled".
func TestBundleViewsFollowTheUnitSwitch(t *testing.T) {
	table := NewTable()
	bundle := installSample(t, table, "parity", "1.0.0")
	if len(bundle.Units) != 5 {
		t.Fatalf("sample bundle has %d units, want 5 (one per kind except mcp)", len(bundle.Units))
	}

	target := bundle.Units[0]
	if _, err := table.SetEnabled(target.ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	fromID, ok := table.Bundle("parity")
	if !ok {
		t.Fatal("Bundle(parity) missing after the switch")
	}
	listed := table.Bundles()
	if len(listed) != 1 {
		t.Fatalf("Bundles() returned %d, want 1", len(listed))
	}
	for _, view := range []*Bundle{fromID, listed[0]} {
		for _, u := range view.Units {
			want := u.ID != target.ID
			if u.Enabled != want {
				t.Fatalf("bundle view of %s: enabled=%v, want %v (the unit map is the authority)", u.ID, u.Enabled, want)
			}
		}
	}

	// One answer across all three views, not three: the map, the kind index and the bundle view.
	for _, u := range table.Units(target.Kind) {
		if u.ID == target.ID && u.Enabled {
			t.Fatal("Units(kind) still reports the switched-off unit as enabled")
		}
	}
	if u, ok := table.Unit(target.ID); !ok || u.Enabled {
		t.Fatalf("Unit(%s) after the switch: %+v ok=%v", target.ID, u, ok)
	}

	if _, err := table.SetEnabled(target.ID, true); err != nil {
		t.Fatalf("SetEnabled back: %v", err)
	}
	again, _ := table.Bundle("parity")
	for _, u := range again.Units {
		if !u.Enabled {
			t.Fatalf("switching back on did not reach the bundle view: %s", u.ID)
		}
	}
}
