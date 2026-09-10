package engine

import (
	"bytes"
	"runtime"
	"slices"
	"sort"
	"sync/atomic"
)

type DeltaNode struct {
	key         []byte
	value       []byte
	isTombstone bool
	next        *DeltaNode
}

type ChainNode struct {
	head            atomic.Pointer[DeltaNode]
	base            atomic.Pointer[LeafNode]
	chainLen        atomic.Int64
	next            *ChainNode
	isConsolidating atomic.Bool
}

const defaultDeltaThreshold = 64
const maxBaseKeys = 256

func NewChainNode() *ChainNode {
	node := &ChainNode{}
	node.base.Store(NewLeafNode())
	node.head.Store(nil)
	node.chainLen.Store(0)
	node.isConsolidating.Store(false)
	return node
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
	for {
		oldHead := c.head.Load()
		newNode := NewDeltaNode(key, value, false, oldHead)
		if c.head.CompareAndSwap(oldHead, newNode) {
			c.chainLen.Add(1)
			break
		}
	}

	if c.chainLen.Load() >= defaultDeltaThreshold && c.isConsolidating.CompareAndSwap(false, true) {
		go func() {
			defer c.isConsolidating.Store(false)
			c.Consolidation()
		}()
	}

	if len(c.base.Load().key) > maxBaseKeys {
		pivot, right := c.Split()
		return pivot, right, nil
	}
	return nil, nil, nil

}

func (c *ChainNode) Delete(key []byte) bool {
	for {
		oldHead := c.head.Load()
		newNode := NewDeltaNode(key, nil, true, oldHead)
		if c.head.CompareAndSwap(oldHead, newNode) {
			c.chainLen.Add(1)
			break
		}
	}

	if c.chainLen.Load() >= defaultDeltaThreshold && c.isConsolidating.CompareAndSwap(false, true) {
		go func() {
			defer c.isConsolidating.Store(false)
			c.Consolidation()
		}()
	}

	return true

}

func (c *ChainNode) Get(key []byte) ([]byte, bool) {
	header := c.head.Load()
	for header != nil {
		if bytes.Equal(header.key, key) {
			if header.isTombstone {
				return nil, false
			}
			return header.value, true
		}
		header = header.next
	}
	return c.base.Load().Get(key)
}

func (c *ChainNode) Consolidation() {
	snapshot := c.head.Load()
	if snapshot == nil {
		return
	}

	seen := make(map[string]bool)
	type update struct {
		key   []byte
		value []byte
	}

	var pendingUpdates []update

	curr := snapshot

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

	currentBase := c.base.Load()
	for i := 0; i < len(currentBase.key); i++ {
		basekey := currentBase.key[i]
		if !seen[string(basekey)] {
			pendingUpdates = append(pendingUpdates, update{
				key:   basekey,
				value: currentBase.value[i],
			})
		}
	}

	slices.SortFunc(pendingUpdates, func(a, b update) int {
		return bytes.Compare(a.key, b.key)
	})

	newBase := &LeafNode{
		key:   make([][]byte, len(pendingUpdates)),
		value: make([][]byte, len(pendingUpdates)),
		next:  nil,
	}

	for i, rec := range pendingUpdates {
		newBase.key[i] = rec.key
		newBase.value[i] = rec.value
	}

	c.base.Store(newBase)

	maxRetries := 3
	retries := 0

	for {
		if retries >= maxRetries {
			return
		}
		livehead := c.head.Load()
		var targetHead *DeltaNode
		var gapNodes []*DeltaNode

		if livehead == snapshot {
			targetHead = nil
		} else {

			curr := livehead

			for curr != nil && curr != snapshot {
				gapNodes = append(gapNodes, curr)
				curr = curr.next
			}

			if curr == nil {
				return
			}

			var previousClonedNode *DeltaNode = nil
			for i := len(gapNodes) - 1; i >= 0; i-- {
				original := gapNodes[i]
				clonedNode := NewDeltaNode(original.key, original.value, original.isTombstone, previousClonedNode)
				previousClonedNode = clonedNode
			}
			targetHead = previousClonedNode
		}

		if c.head.CompareAndSwap(livehead, targetHead) {
			c.chainLen.Store(int64(len(gapNodes)))
			return
		}
		retries++
		runtime.Gosched()
	}
}

func (c *ChainNode) Split() (pivotKey []byte, rightNode *ChainNode) {

	for !c.isConsolidating.CompareAndSwap(false, true) {
		runtime.Gosched()
	}
	defer c.isConsolidating.Store(false)

	c.Consolidation()
	currentBase := c.base.Load()

	mid := len(currentBase.key) / 2
	pivotKey = currentBase.key[mid]

	rightNode = NewChainNode()
	rightBase := rightNode.base.Load()

	rightBase.key = append(rightBase.key, currentBase.key[mid:]...)
	rightBase.value = append(rightBase.value, currentBase.value[mid:]...)

	leftBase := NewLeafNode()
	leftBase.key = append(leftBase.key, currentBase.key[:mid]...)
	leftBase.value = append(leftBase.value, currentBase.value[:mid]...)

	c.base.Store(leftBase)

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
	curr := c.head.Load()
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

	currentBase := c.base.Load()
	baseIdx := 0
	if len(start) > 0 {
		baseIdx = sort.Search(len(currentBase.key), func(i int) bool {
			return bytes.Compare(currentBase.key[i], start) >= 0
		})
	}

	var basePairs []KVPair
	for i := baseIdx; i < len(currentBase.key); i++ {
		bKey := currentBase.key[i]

		if len(end) > 0 && bytes.Compare(bKey, end) >= 0 {
			reachedEnd = true
			break
		}

		if seen[string(bKey)] {
			continue
		}

		basePairs = append(basePairs, KVPair{
			Key:   bKey,
			Value: currentBase.value[i],
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
