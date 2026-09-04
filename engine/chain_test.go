package engine

import (
	"bytes"
	"fmt"
	"testing"
)

func TestChainNode(t *testing.T) {
	chainNode := NewChainNode()

	testdata := []struct {
		key   string
		value string
	}{
		{"cherry", "value_cherry"},
		{"apple", "value_apple"},
		{"banana", "value_banana"},
	}

	for _, td := range testdata {
		_, err := chainNode.Put([]byte(td.key), []byte(td.value))
		if err != nil {
			t.Fatalf("Failed to Put key %q: %v", td.key, err)
		}
	}

	for _, tc := range testdata {
		val, found := chainNode.Get([]byte(tc.key))
		if !found {
			t.Errorf("Expected to find key %q, but it was not found", tc.key)
			continue
		}
		if !bytes.Equal(val, []byte(tc.value)) {
			t.Errorf("For key %q, expected value %q, got %q", tc.key, tc.value, string(val))
		}
	}

	for _, td := range testdata {
		val, found := chainNode.Get([]byte(td.key))
		if !found {
			t.Errorf("After Put, key %q was not found", td.key)
			continue
		}
		if !bytes.Equal(val, []byte(td.value)) {
			t.Errorf("After Put, key %q has value %q, expected %q", td.key, string(val), td.value)
		}
	}

	deleted := chainNode.Delete([]byte("apple"))
	if !deleted {
		t.Errorf("Expected Delete to return true for existing key \"apple\"")
	}
	_, found := chainNode.Get([]byte("apple"))
	if found {
		t.Error("Expected key \"apple\" to be missing after deletion, but it was found")
	}
}

func TestChainNode_Consolidation(t *testing.T) {
	chainNode := NewChainNode()

	// Step 1: Populate base with initial values
	testdata1 := []struct {
		key   string
		value string
	}{
		{"grape", "purple"},
		{"apple", "red"},
	}
	for _, tc := range testdata1 {
		err := chainNode.base.Put([]byte(tc.key), []byte(tc.value))
		if err != nil {
			t.Fatalf("Failed to Put key %q to base: %v", tc.key, err)
		}
	}

	// Step 2: Add deltas
	testdata2 := []struct {
		key   string
		value string
	}{
		{"apple", "green"},
		{"banana", "yellow"},
	}

	for _, td := range testdata2 {
		_, err := chainNode.Put([]byte(td.key), []byte(td.value))
		if err != nil {
			t.Fatalf("Failed to Put key %q: %v", td.key, err)
		}
	}

	// Step 3: Delete apple (creates tombstone)
	deleted := chainNode.Delete([]byte("apple"))
	if !deleted {
		t.Errorf("Expected Delete to return true for \"apple\"")
	}

	// Step 4: Consolidate
	chainNode.Consolidation()

	// Assert: head should be nil after consolidation
	if chainNode.head != nil {
		t.Errorf("Expected chainNode.head to be nil after Consolidation, but it is not")
	}

	// Assert: apple should not exist (tombstone was honored)
	apple, appleFound := chainNode.base.Get([]byte("apple"))
	if appleFound {
		t.Errorf("Expected apple to be deleted, but found value %q", string(apple))
	}

	// Assert: banana should be "yellow"
	banana, bananaFound := chainNode.base.Get([]byte("banana"))
	if !bananaFound {
		t.Errorf("Expected to find banana after consolidation, but it was not found")
	} else if string(banana) != "yellow" {
		t.Errorf("Expected banana to be \"yellow\", got %q", string(banana))
	}

	// Assert: grape should be "purple" (survived from original base)
	grape, grapeFound := chainNode.base.Get([]byte("grape"))
	if !grapeFound {
		t.Errorf("Expected to find grape after consolidation, but it was not found")
	} else if string(grape) != "purple" {
		t.Errorf("Expected grape to be \"purple\", got %q", string(grape))
	}
}

func TestChainNode_AutoConsolidation(t *testing.T) {
	chainNode := NewChainNode()

	// Step 1: Insert keys 0-6 (7 operations)
	for i := 0; i < 7; i++ {
		keyString := fmt.Sprintf("%d", i)
		keyByte := []byte(keyString)
		_, err := chainNode.Put(keyByte, keyByte)
		if err != nil {
			t.Fatalf("Failed to Put key %q: %v", keyString, err)
		}
	}

	// Assert: chainLen should be 7 and head should not be nil
	if chainNode.chainLen != 7 {
		t.Errorf("After 7 Puts, expected chainLen == 7, got %d", chainNode.chainLen)
	}
	if chainNode.head == nil {
		t.Errorf("After 7 Puts, expected chainNode.head to be non-nil")
	}

	// Step 2: Insert key 7 (8th operation - triggers auto-consolidation)
	_, err := chainNode.Put([]byte("7"), []byte("7"))
	if err != nil {
		t.Fatalf("Failed to Put key 7: %v", err)
	}

	// Assert: After consolidation, chainLen should be 0 and head should be nil
	if chainNode.chainLen != 0 {
		t.Errorf("After auto-consolidation, expected chainLen == 0, got %d", chainNode.chainLen)
	}
	if chainNode.head != nil {
		t.Errorf("After auto-consolidation, expected chainNode.head to be nil")
	}

	// Step 3: Verify all 8 keys are retrievable
	for i := 0; i < 8; i++ {
		keyString := fmt.Sprintf("%d", i)
		val, found := chainNode.Get([]byte(keyString))
		if !found {
			t.Errorf("Expected to find key %q after consolidation, but it was not found", keyString)
			continue
		}
		if !bytes.Equal(val, []byte(keyString)) {
			t.Errorf("For key %q, expected value %q, got %q", keyString, keyString, string(val))
		}
	}
}

func BenchmarkChainNode_PutReverse_1M(b *testing.B) {
	chainNode := NewChainNode()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		index := 1000000 - 1 - (i % 1000000)

		keyString := fmt.Sprintf("%02d", index)
		keyByte := []byte(keyString)

		_, _ = chainNode.Put(keyByte, keyByte)
	}
}

func BenchmarkChainNode_Get(b *testing.B) {
	chainNode := NewChainNode()

	// Populate base page with 1,000 keys
	for i := 0; i < 1000; i++ {
		keyString := fmt.Sprintf("%04d", i)
		keyByte := []byte(keyString)
		_ = chainNode.base.Put(keyByte, keyByte)
	}

	// Add deltas to build a chain (simulate multiple updates)
	for i := 0; i < 100; i++ {
		keyString := fmt.Sprintf("%04d", i%1000)
		keyByte := []byte(keyString)
		_, _ = chainNode.Put(keyByte, []byte("delta_value"))
	}

	b.ResetTimer()

	// Benchmark Get operations on keys in the base page
	// This forces full chain traversal before finding the key in base
	for i := 0; i < b.N; i++ {
		keyIndex := i % 1000
		keyString := fmt.Sprintf("%04d", keyIndex)
		keyByte := []byte(keyString)
		_, _ = chainNode.Get(keyByte)
	}
}
