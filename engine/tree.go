package engine

import (
	"bytes"
	"sort"
)

// InternalNode routes searches to the correct child node.
// It does not store values, only pivot keys and child pointers.
type InternalNode struct {
	keys     [][]byte      // N pivot keys
	children []interface{} // N+1 children (can be *InternalNode or *SlottedPage)
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
