package auth

import "testing"

// TestGroupsChanged_DuplicateIncomingMasksRemoval proves a repeated group
// value in the incoming header cannot hide the loss of a different stored
// group just because the two raw slice lengths happen to match. A proxy that
// re-asserts one group twice (a duplicated header value, or a group name
// sent by more than one upstream attribute) must not defeat revocation of a
// group the user no longer has.
func TestGroupsChanged_DuplicateIncomingMasksRemoval(t *testing.T) {
	store := newFakeStore()
	store.storedGroups = []string{"admins", "viewers"}

	changed, err := groupsChanged(store, 1, []string{"admins", "admins"})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("duplicated incoming group masked the loss of \"viewers\": groupsChanged reported no change")
	}
}

// TestGroupsChanged_ReorderedMatchIsUnchanged proves groupsChanged compares
// membership, not order: the same set presented in a different sequence is
// not a change.
func TestGroupsChanged_ReorderedMatchIsUnchanged(t *testing.T) {
	store := newFakeStore()
	store.storedGroups = []string{"admins", "viewers"}

	changed, err := groupsChanged(store, 1, []string{"viewers", "admins"})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("reordering only, groupsChanged must report unchanged")
	}
}

// TestGroupsChanged_DuplicateStoredStillDetectsAddition guards the other
// direction: even if a stored snapshot somehow carried a duplicate, an
// incoming group absent from storage must still register as a change.
func TestGroupsChanged_DuplicateStoredStillDetectsAddition(t *testing.T) {
	store := newFakeStore()
	store.storedGroups = []string{"admins", "admins"}

	changed, err := groupsChanged(store, 1, []string{"admins", "viewers"})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("new group \"viewers\" not detected as a change")
	}
}
