package engine

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

func TestTree_RootSplit(t *testing.T) {
	tree := &Tree{}

	// Insert enough keys to trigger leaf and internal-node splits.
	keys := make([]string, 100)
	for i := range keys {
		keys[i] = fmt.Sprintf("key%04d", i)
	}

	for i, k := range keys {
		err := tree.Put([]byte(k), []byte("val_"+k))
		if err != nil {
			t.Fatalf("Failed to put %s: %v", k, err)
		}

		// Allow the background consolidation worker to process the delta chain.
		// Once it populates the base page, a subsequent Put will detect
		// the size limit and push the split up to the Tree.
		if i > 0 && i%8 == 0 {
			time.Sleep(5 * time.Millisecond)
		}
	}

	// Force one last foreground Put to evaluate any remaining oversized base pages
	// and trigger the final splits up the tree.
	tree.Put([]byte("trigger_split"), []byte("val"))

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

func BenchmarkTree_Put(b *testing.B) {
	t := &Tree{}
	val := []byte("benchmark_value")

	// Pre-generate keys so we don't benchmark string allocation
	keys := make([][]byte, b.N)
	for i := 0; i < b.N; i++ {
		keys[i] = []byte(fmt.Sprintf("key-%08x", rand.Int31()))
	}

	b.ResetTimer() // Start the clock here

	for i := 0; i < b.N; i++ {
		t.Put(keys[i], val)
	}
}

func BenchmarkTree_Get(b *testing.B) {
	t := &Tree{}
	val := []byte("benchmark_value")

	// Insert 100,000 keys to build a realistic tree
	numKeys := 100000
	keys := make([][]byte, numKeys)
	for i := 0; i < numKeys; i++ {
		keys[i] = []byte(fmt.Sprintf("key-%08d", i))
		t.Put(keys[i], val)
	}

	b.ResetTimer()

	// Benchmark random reads
	for i := 0; i < b.N; i++ {
		idx := i % numKeys
		_, found := t.Get(keys[idx])
		if !found {
			b.Fatalf("key missing: %s", keys[idx])
		}
	}
}

func BenchmarkTree_Scan(b *testing.B) {
	t := &Tree{}
	val := []byte("benchmark_value")

	// Insert 100,000 keys sequentially
	numKeys := 1000
	for i := 0; i < numKeys; i++ {
		t.Put([]byte(fmt.Sprintf("key-%08d", i)), val)
	}

	b.ResetTimer()

	// Benchmark scanning ranges of 100 keys
	for i := 0; i < b.N; i++ {
		// Pick a random starting point that leaves room for 100 keys
		startIdx := rand.Intn(numKeys - 100)
		startKey := []byte(fmt.Sprintf("key-%08d", startIdx))
		endKey := []byte(fmt.Sprintf("key-%08d", startIdx+100))

		pairs, err := t.Scan(startKey, endKey)
		if err != nil {
			b.Fatal(err)
		}
		if len(pairs) == 0 {
			b.Fatalf("scan returned empty for range %s - %s", startKey, endKey)
		}
	}
}
