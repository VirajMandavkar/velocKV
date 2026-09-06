package engine

import (
	"testing"
)

func TestTree_RootSplit(t *testing.T) {
	tree := &Tree{}

	// Insert enough keys to trigger a leaf split
	for _, k := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"} {
		err := tree.Put([]byte(k), []byte("val_"+k))
		if err != nil {
			t.Fatalf("Failed to put %s: %v", k, err)
		}
	}

	// Verify root promoted to an InternalNode
	if _, ok := tree.root.(*InternalNode); !ok {
		t.Fatalf("Expected tree.root to be *InternalNode after split, got %T", tree.root)
	}

	// Verify all keys remain accessible
	for _, k := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"} {
		val, found := tree.Get([]byte(k))
		if !found {
			t.Errorf("Expected to find key %s", k)
		}
		if string(val) != "val_"+k {
			t.Errorf("Key %s: expected val_%s, got %s", k, k, string(val))
		}
	}
}
