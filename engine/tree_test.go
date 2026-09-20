package engine

import (
	"fmt"
	"testing"
)

func TestTree_Integration_PutAndScan(t *testing.T) {
	tree := NewTree()
	numRecords := 5000 // High enough to force multiple page splits and internal node growth

	// 1. Insert 5000 records sequentially
	for i := 0; i < numRecords; i++ {
		key := []byte(fmt.Sprintf("user_%05d", i))
		val := []byte(fmt.Sprintf("data_%05d", i))
		tree.Put(key, val, 0)
	}

	// 2. Point Lookup Verification
	val, _, found := tree.Get([]byte("user_02500"), 0)
	if !found || string(val) != "data_02500" {
		t.Fatalf("Failed to retrieve key 'user_02500', got: %s", string(val))
	}

	// 3. Range Scan Verification (Spans multiple 4KB pages)
	startKey := []byte("user_03000")
	endKey := []byte("user_03100")

	results := tree.Scan(startKey, endKey, 0)

	// Should return exactly 100 records
	if len(results) != 100 {
		t.Fatalf("Expected 100 records in scan, got %d", len(results))
	}

	// Verify the bounds of the scan
	if string(results[0].Key) != "user_03000" {
		t.Errorf("Expected first key to be user_03000, got %s", string(results[0].Key))
	}
	if string(results[99].Key) != "user_03099" {
		t.Errorf("Expected last key to be user_03099, got %s", string(results[99].Key))
	}
}
