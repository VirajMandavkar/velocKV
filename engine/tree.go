package engine

import "bytes"

type Node interface {
	Get(key []byte) ([]byte, bool)
	Put(key, value []byte) (pivot []byte, newChild Node, err error)
}

type InternalNode struct {
	key      [][]byte
	children []Node
}

type Tree struct {
	root Node
}

const maxInternalKeys = 5

func (in *InternalNode) Get(key []byte) ([]byte, bool) {

	for i := 0; i < len(in.key); i++ {
		comp := bytes.Compare(key, in.key[i])
		if comp < 0 {
			return in.children[i].Get(key)
		}
	}
	if len(in.children) > len(in.key) {
		return in.children[len(in.key)].Get(key)
	}
	return nil, false
}

func (in *InternalNode) Put(key, value []byte) ([]byte, Node, error) {
	childIdx := len(in.key)
	for i := 0; i < len(in.key); i++ {
		if bytes.Compare(key, in.key[i]) < 0 {
			childIdx = i
			break
		}
	}

	pivot, newChild, err := in.children[childIdx].Put(key, value)
	if err != nil {
		return nil, nil, err
	}
	if newChild == nil {
		return nil, nil, nil
	}

	in.key = append(in.key, nil)
	copy(in.key[childIdx+1:], in.key[childIdx:])
	in.key[childIdx] = pivot

	in.children = append(in.children, nil)
	copy(in.children[childIdx+2:], in.children[childIdx+1:])
	in.children[childIdx+1] = newChild

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

func (t *Tree) Get(key []byte) ([]byte, bool) {
	if t.root == nil {
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
