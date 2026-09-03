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
