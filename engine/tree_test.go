package engine

import (
	"fmt"
	"testing"
)

func TestTree_RootSplit(t *testing.T) {
	tree := &Tree{}

	// Insert enough keys to trigger leaf and internal-node splits.
	keys := make([]string, 100)
	for i := range keys {
		keys[i] = fmt.Sprintf("key%04d", i)
	}

	for _, k := range keys {
		err := tree.Put([]byte(k), []byte("val_"+k))
		if err != nil {
			t.Fatalf("Failed to put %s: %v", k, err)
		}
	}

	// Verify the root and its children form a height-3 tree.
	root, ok := tree.root.(*InternalNode)
	if !ok {
		t.Fatalf("Expected tree.root to be *InternalNode after split, got %T", tree.root)
	}
	if len(root.children) < 2 {
		t.Fatalf("Expected root to have multiple children, got %d", len(root.children))
	}

	for childIndex, child := range root.children {
		internal, ok := child.(*InternalNode)
		if !ok {
			t.Fatalf("Expected root child %d to be *InternalNode, got %T", childIndex, child)
		}
		if len(internal.children) == 0 {
			t.Fatalf("Expected root child %d to have leaf children", childIndex)
		}
		for leafIndex, leaf := range internal.children {
			if _, ok := leaf.(*ChainNode); !ok {
				t.Fatalf("Expected root child %d leaf %d to be *ChainNode, got %T", childIndex, leafIndex, leaf)
			}
		}
	}

	// Verify all keys remain accessible
	for _, k := range keys {
		val, found := tree.Get([]byte(k))
		if !found {
			t.Errorf("Expected to find key %s", k)
		}
		if string(val) != "val_"+k {
			t.Errorf("Key %s: expected val_%s, got %s", k, k, string(val))
		}
	}
}
