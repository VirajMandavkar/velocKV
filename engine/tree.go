package engine

import (
	"bytes"
	"sort"
)

const MaxInternalKeys = 64

// InternalNode routes searches to the correct child node.
// It does not store values, only pivot keys and child pointers.
type InternalNode struct {
	keys     [][]byte      // N pivot keys
	children []interface{} // N+1 children (can be *InternalNode or *SlottedPage)
}

// KVPair represents a result from a range scan
type KVPair struct {
	Key []byte
	Val []byte
}

// Tree represents the full index.
type Tree struct {
	root interface{} // Points to either a *SlottedPage (if small) or an *InternalNode
}

// Route performs a binary search to find the correct child index for a given key.
func (in *InternalNode) Route(key []byte) int {
	return sort.Search(len(in.keys), func(i int) bool {
		return bytes.Compare(key, in.keys[i]) < 0
	})
}

func (t *Tree) Get(fullkey []byte) ([]byte, bool, bool) {
	if t.root == nil {
		return nil, false, false
	}

	curr := t.root
	for {
		switch node := curr.(type) {
		case *InternalNode:
			childIdx := node.Route(fullkey)
			curr = node.children[childIdx]
		case *SlottedPage:
			return node.Get(fullkey)
		default:
			panic("unknown node tyoe in tree")
		}
	}
}

// Put inserts or updates a key-value pair in the tree.
func (t *Tree) Put(key, val []byte) {
	if t.root == nil {
		// Initialize the first leaf page
		rootPage := &SlottedPage{}
		rootPage.SetSlotCount(0)
		rootPage.SetFreeSpace(PageSize - HeaderSize)

		// Start with no prefix so we don't immediately reject the second key
		rootPage.SetPrefixLen(0)

		// Insert the record as a full raw key
		rootPage.InsertRecord(key, val, false, 0)
		t.root = rootPage
		return
	}

	pivot, newChild := putRecursive(t.root, key, val)
	if newChild != nil {
		// The old root split! Grow the tree upwards.
		newRoot := &InternalNode{
			keys:     [][]byte{pivot},
			children: []interface{}{t.root, newChild},
		}
		t.root = newRoot
	}
}

// putRecursive descends the tree, applies the mutation to the leaf,
// and propagates splits back up the call stack.
func putRecursive(node interface{}, key, val []byte) (pivot []byte, rightNode interface{}) {
	switch n := node.(type) {
	case *InternalNode:
		idx := n.Route(key)
		childPivot, childRight := putRecursive(n.children[idx], key, val)

		if childRight != nil {
			// A child split occurred. Insert the new pivot and right child into this node.
			n.keys = insertBytes(n.keys, idx, childPivot)
			n.children = insertChild(n.children, idx+1, childRight)

			// If this internal node overflows, split it
			if len(n.keys) > MaxInternalKeys {
				mid := len(n.keys) / 2
				upPivot := n.keys[mid]

				rightInternal := &InternalNode{
					keys:     append([][]byte(nil), n.keys[mid+1:]...),
					children: append([]interface{}(nil), n.children[mid+1:]...),
				}

				n.keys = n.keys[:mid]
				n.children = n.children[:mid+1]

				return upPivot, rightInternal
			}
		}
		return nil, nil

	case *SlottedPage:
		// Attempt in-place or out-of-place update first
		suffix, match := n.SplitKey(key)
		if match {
			if n.UpdateRecord(suffix, val, false, 0) {
				return nil, nil
			}
			// If update failed due to space, fall through to split
		}

		// Attempt insert
		if match && n.InsertRecord(suffix, val, false, 0) {
			return nil, nil
		}

		// 4KB Page is full (or prefix diverged). Split the page.
		leafPivot, rightLeaf := n.Split()

		// The new key must be inserted into whichever half it belongs
		cmp := bytes.Compare(key, leafPivot)
		if cmp < 0 {
			insertIntoLeaf(n, key, val)
		} else {
			insertIntoLeaf(rightLeaf, key, val)
		}

		return leafPivot, rightLeaf

	default:
		panic("unknown node type")
	}
}

// Helper to force an insert into a specific leaf, adjusting its prefix if necessary.
// For simplicity in this step, we temporarily reset the prefix to nil if it diverges.
func insertIntoLeaf(p *SlottedPage, key, val []byte) {
	suffix, match := p.SplitKey(key)
	if !match {
		// In a full implementation, we'd calculate the new common prefix and physically shift existing suffixes.
		// For now, clear the base prefix so it accepts the new key as a raw suffix.
		p.SetPrefixLen(0)
		suffix = key
	}
	p.InsertRecord(suffix, val, false, 0)
}

// Slice manipulation helpers for InternalNode
func insertBytes(slice [][]byte, idx int, val []byte) [][]byte {
	slice = append(slice, nil)
	copy(slice[idx+1:], slice[idx:])
	slice[idx] = val
	return slice
}

func insertChild(slice []interface{}, idx int, val interface{}) []interface{} {
	slice = append(slice, nil)
	copy(slice[idx+1:], slice[idx:])
	slice[idx] = val
	return slice
}

// Scan retrieves all key-value pairs in the range [startKey, endKey).
func (t *Tree) Scan(startKey, endKey []byte) []KVPair {
	if t.root == nil {
		return nil
	}

	// 1. Route down to the first leaf
	curr := t.root
	for {
		if node, ok := curr.(*InternalNode); ok {
			idx := node.Route(startKey)
			curr = node.children[idx]
		} else {
			break
		}
	}

	leaf := curr.(*SlottedPage)
	var results []KVPair

	// 2. Horizontal Traversal (The B-Link magic)
	for leaf != nil {
		slotCount := leaf.GetSlotCount()

		for i := uint16(0); i < slotCount; i++ {
			rawSlot := leaf.GetSlot(i)
			offset, isExt, sufLen, valLen := UnpackSlot(rawSlot)

			if valLen == 0 {
				continue // Skip tombstones
			}

			// Reconstruct the full key to check bounds
			suffix := leaf.data[offset : offset+uint32(sufLen)]
			fullKey, _ := leaf.AssembleFullKey(i, suffix)

			if bytes.Compare(fullKey, startKey) < 0 {
				continue
			}
			if bytes.Compare(fullKey, endKey) >= 0 {
				return results // Crossed the upper bound, scan complete
			}

			// Extract value
			valOffset := offset + uint32(sufLen)
			var val []byte
			if isExt {
				val = leaf.data[valOffset : valOffset+8] // Arena Handle
			} else {
				val = make([]byte, valLen)
				copy(val, leaf.data[valOffset:valOffset+uint32(valLen)])
			}

			// We must copy fullKey so the slice doesn't point to the raw page memory
			keyCopy := make([]byte, len(fullKey))
			copy(keyCopy, fullKey)

			results = append(results, KVPair{Key: keyCopy, Val: val})
		}

		// 3. Hop to the right sibling using the header pointer
		nextPtr := leaf.GetRightSibling()
		if nextPtr == nil {
			break
		}
		leaf = leaf.GetRightSibling()
	}

	return results
}
