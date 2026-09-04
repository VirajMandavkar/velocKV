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

func BenchmarkChainNode_PutReverse(b *testing.B) {
	chainNode := NewChainNode()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		index := 1000000 - 1 - (i % 1000000)

		keyString := fmt.Sprintf("%02d", index)
		keyByte := []byte(keyString)

		_, _ = chainNode.Put(keyByte, keyByte)
	}
}
