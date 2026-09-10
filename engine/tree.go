package engine

import (
	"bytes"
	"slices"
	"sort"
)

type Node interface {
	Get(key []byte) ([]byte, bool)
	Put(key, value []byte) (pivot []byte, newChild Node, err error)
	FindLeaf(start []byte) *ChainNode
}

type InternalNode struct {
	key      [][]byte
	children []Node
}

type Tree struct {
	root Node
}

type KVPair struct {
	Key   []byte
	Value []byte
}

const maxInternalKeys = 64

func (in *InternalNode) route(key []byte) int {
	return sort.Search(len(in.key), func(i int) bool {
		return bytes.Compare(key, in.key[i]) < 0
	})
}

func (in *InternalNode) Get(key []byte) ([]byte, bool) {

	childIdx := in.route(key)
	if childIdx < len(in.children) {
		return in.children[childIdx].Get(key)
	}
	return nil, false
}

func (in *InternalNode) Put(key, value []byte) ([]byte, Node, error) {

	childIdx := in.route(key)

	pivot, newChild, err := in.children[childIdx].Put(key, value)
	if err != nil {
		return nil, nil, err
	}
	if newChild == nil {
		return nil, nil, nil
	}

	in.key = slices.Insert(in.key, childIdx, pivot)
	in.children = slices.Insert(in.children, childIdx+1, newChild)

	if len(in.key) > maxInternalKeys {
		mid := len(in.key) / 2
		pivot := in.key[mid]

		rightInternalNode := &InternalNode{
			key:      append([][]byte(nil), in.key[mid+1:]...),
			children: append([]Node(nil), in.children[mid+1:]...),
		}

		in.key = in.key[:mid]
		in.children = in.children[:mid+1]

		return pivot, rightInternalNode, nil
	}

	return nil, nil, nil
}

func (in *InternalNode) FindLeaf(start []byte) *ChainNode {
	childIdx := in.route(start)
	if childIdx < len(in.children) {
		return in.children[childIdx].FindLeaf(start)
	}
	return nil
}

func (t *Tree) Get(key []byte) ([]byte, bool) {
	if t.root == nil || len(key) == 0 {
		return nil, false
	}

	return t.root.Get(key)
}

func (t *Tree) Put(key, value []byte) error {
	if t.root == nil {
		t.root = NewChainNode()
	}

	pivot, newChild, err := t.root.Put(key, value)
	if err != nil {
		return err
	}
	if newChild == nil {
		return nil
	}

	newRoot := &InternalNode{
		key:      [][]byte{pivot},
		children: []Node{t.root, newChild},
	}
	t.root = newRoot

	return nil
}

func (t *Tree) Scan(start, end []byte) ([]KVPair, error) {
	if len(end) > 0 && bytes.Compare(start, end) >= 0 {
		return nil, nil
	}
	if t.root == nil {
		return []KVPair{}, nil
	}

	targetLeaf := t.root.FindLeaf(start)
	results := make([]KVPair, 0)
	firstLeaf := true

	for targetLeaf != nil {
		leafStart := start
		if !firstLeaf {
			leafStart = nil
		}

		pairs, reachedEnd := targetLeaf.ScanLeaf(leafStart, end)
		results = append(results, pairs...)

		if reachedEnd {
			break
		}

		firstLeaf = false
		targetLeaf = targetLeaf.next
	}

	return results, nil
}
