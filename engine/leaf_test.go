package engine

import (
	"bytes"
	"testing"
)

func TestLeafNode_BasicOperations(t *testing.T) {
	leafNode := NewLeafNode()

	testdata := []struct {
		key   string
		value string
	}{
		{"cherry", "value_cherry"},
		{"apple", "value_apple"},
		{"banana", "value_banana"},
	}

	for _, tc := range testdata {
		err := leafNode.Put([]byte(tc.key), []byte(tc.value))
		if err != nil {
			t.Fatalf("Failed to Put key %q: %v", tc.key, err)
		}
	}

	for _, tc := range testdata {
		val, found := leafNode.Get([]byte(tc.key))
		if !found {
			t.Errorf("Expected to find key %q, but it was not found", tc.key)
			continue
		}
		if !bytes.Equal(val, []byte(tc.value)) {
			t.Errorf("For key %q, expected value %q, got %q", tc.key, tc.value, string(val))
		}
	}

	expectedOrder := []string{"apple", "banana", "cherry"}
	if len(leafNode.key) != len(expectedOrder) {
		t.Errorf("Expected leaf length to be %d, got %d", len(expectedOrder), len(leafNode.key))
	} else {
		for i, expectedKey := range expectedOrder {
			if !bytes.Equal(leafNode.key[i], []byte(expectedKey)) {
				t.Errorf("Sorting order mismatch at index %d. Expected %q, got %q", i, expectedKey, string(leafNode.key[i]))
			}
		}
	}

	deleted := leafNode.Delete([]byte("apple"))
	if !deleted {
		t.Errorf("Expected Delete to return true for existing key \"apple\"")
	}

	_, found := leafNode.Get([]byte("apple"))
	if found {
		t.Error("Expected key \"apple\" to be missing after deletion, but it was found")
	}

}
