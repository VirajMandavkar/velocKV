package engine

import "bytes"

type DeltaNode struct {
	key         []byte
	value       []byte
	isTombstone bool
	next        *DeltaNode
}

type ChainNode struct {
	head *DeltaNode
	base *LeafNode
}

func NewChainNode() *ChainNode {
	return &ChainNode{
		head: nil,
		base: NewLeafNode(),
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

func (c *ChainNode) Put(key []byte, value []byte) (bool, error) {
	newNode := NewDeltaNode(key, value, false, c.head)
	c.head = newNode
	return true, nil
}

func (c *ChainNode) Delete(key []byte) bool {
	tombNode := NewDeltaNode(key, nil, true, c.head)
	c.head = tombNode
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
}
