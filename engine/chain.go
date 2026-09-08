package engine

import (
	"bytes"
	"slices"
	"sort"
)

type DeltaNode struct {
	key         []byte
	value       []byte
	isTombstone bool
	next        *DeltaNode
}

type ChainNode struct {
	head     *DeltaNode
	base     *LeafNode
	chainLen int
	next     *ChainNode
}

const defaultDeltaThreshold = 8
const maxBaseKeys = 8

func NewChainNode() *ChainNode {
	return &ChainNode{
		head:     nil,
		base:     NewLeafNode(),
		chainLen: 0,
	}
}

func NewDeltaNode(key []byte, value []byte, isTombstone bool, next *DeltaNode) *DeltaNode {
	return &DeltaNode{
		key:         key,
		value:       value,
		isTombstone: isTombstone,
		next:        next,
	}
}

func (c *ChainNode) Put(key []byte, value []byte) (pivot []byte, newChild Node, err error) {
	newNode := NewDeltaNode(key, value, false, c.head)
	c.head = newNode
	c.chainLen++
	if c.chainLen >= defaultDeltaThreshold {
		c.Consolidation()
	}
	if len(c.base.key) > maxBaseKeys {
		pivot, right := c.Split()
		return pivot, right, nil
	}
	return nil, nil, nil
}

// func (c *ChainNode) logicalKeyCount() int {
// 	newKeys := make(map[string]struct{})
// 	for curr := c.head; curr != nil; curr = curr.next {
// 		if _, found := newKeys[string(curr.key)]; found {
// 			continue
// 		}
// 		if _, found := c.base.Get(curr.key); !found {
// 			newKeys[string(curr.key)] = struct{}{}
// 		}
// 	}
// 	return len(c.base.key) + len(newKeys)
// }

func (c *ChainNode) Delete(key []byte) bool {
	tombNode := NewDeltaNode(key, nil, true, c.head)
	c.head = tombNode
	c.chainLen++
	if c.chainLen >= defaultDeltaThreshold {
		c.Consolidation()
	}
	return true
}

func (c *ChainNode) Get(key []byte) ([]byte, bool) {
	header := c.head
	for header != nil {
		comp := bytes.Compare(header.key, key)
		if comp == 0 {
			if header.isTombstone {
				return nil, false
			}
			return header.value, true
		}
		header = header.next
	}
	return c.base.Get(key)
}

func (c *ChainNode) Consolidation() {

	if c.head == nil {
		return
	}
	seen := make(map[string]bool)

	type update struct {
		key   []byte
		value []byte
	}
	var pendingUpdates []update

	curr := c.head
	for curr != nil {
		k := string(curr.key)
		if !seen[k] {
			seen[k] = true
			if !curr.isTombstone {
				pendingUpdates = append(pendingUpdates, update{
					key:   curr.key,
					value: curr.value,
				})
			}
		}
		curr = curr.next
	}

	for i := 0; i < len(c.base.key); i++ {
		basekey := c.base.key[i]
		if !seen[string(basekey)] {
			pendingUpdates = append(pendingUpdates, update{
				key:   basekey,
				value: c.base.value[i],
			})
		}
	}

	newBase := NewLeafNode()

	for _, rec := range pendingUpdates {
		_ = newBase.Put(rec.key, rec.value)
	}

	c.base = newBase
	c.head = nil
	c.chainLen = 0
}

func (c *ChainNode) Split() (pivotKey []byte, rightNode *ChainNode) {
	mid := len(c.base.key) / 2
	pivotKey = c.base.key[mid]
	rightNode = NewChainNode()

	rightNode.base.key = append(rightNode.base.key, c.base.key[mid:]...)
	rightNode.base.value = append(rightNode.base.value, c.base.value[mid:]...)

	c.base.key = c.base.key[:mid]
	c.base.value = c.base.value[:mid]
	rightNode.next = c.next
	c.next = rightNode

	return pivotKey, rightNode
}

func (c *ChainNode) FindLeaf(key []byte) *ChainNode {
	return c
}

func (c *ChainNode) ScanLeaf(start, end []byte) ([]KVPair, bool) {
	reachedEnd := false
	if len(end) > 0 && bytes.Compare(start, end) >= 0 {
		return nil, false
	}
	type deltaEntry struct {
		key         []byte
		value       []byte
		isTombStone bool
	}
	curr := c.head
	seen := make(map[string]bool)
	var activeDeltas []deltaEntry

	for curr != nil {
		k := string(curr.key)
		if !seen[k] {
			seen[k] = true
			inStart := len(start) == 0 || bytes.Compare(curr.key, start) >= 0
			inEnd := len(end) == 0 || bytes.Compare(curr.key, end) < 0

			if inStart && inEnd {
				activeDeltas = append(activeDeltas, deltaEntry{
					key:         curr.key,
					value:       curr.value,
					isTombStone: curr.isTombstone,
				})
			}
			if len(end) > 0 && bytes.Compare(curr.key, end) >= 0 {
				reachedEnd = true
			}
		}
		curr = curr.next
	}

	slices.SortFunc(activeDeltas, func(a, b deltaEntry) int {
		return bytes.Compare(a.key, b.key)
	})

	baseIdx := 0
	if len(start) > 0 {
		baseIdx = sort.Search(len(c.base.key), func(i int) bool {
			return bytes.Compare(c.base.key[i], start) >= 0
		})
	}

	var basePairs []KVPair
	for i := baseIdx; i < len(c.base.key); i++ {
		bKey := c.base.key[i]

		if len(end) > 0 && bytes.Compare(bKey, end) >= 0 {
			reachedEnd = true
			break
		}

		if seen[string(bKey)] {
			continue
		}

		basePairs = append(basePairs, KVPair{
			Key:   bKey,
			Value: c.base.value[i],
		})
	}

	var results []KVPair
	dIdx, bIdx := 0, 0

	for dIdx < len(activeDeltas) && bIdx < len(basePairs) {
		comp := bytes.Compare(activeDeltas[dIdx].key, basePairs[bIdx].Key)
		if comp < 0 {
			if !activeDeltas[dIdx].isTombStone {
				results = append(results, KVPair{
					Key:   activeDeltas[dIdx].key,
					Value: activeDeltas[dIdx].value,
				})
			}
			dIdx++
		} else {
			results = append(results, basePairs[bIdx])
			bIdx++
		}
	}

	for dIdx < len(activeDeltas) {
		if !activeDeltas[dIdx].isTombStone {
			results = append(results, KVPair{
				Key:   activeDeltas[dIdx].key,
				Value: activeDeltas[dIdx].value,
			})
		}
		dIdx++
	}

	for bIdx < len(basePairs) {
		results = append(results, basePairs[bIdx])
		bIdx++
	}

	return results, reachedEnd

}
