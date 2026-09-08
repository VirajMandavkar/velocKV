package engine

import (
	"bytes"
	"errors"
)

type LeafNode struct {
	key   [][]byte
	value [][]byte
	next  *LeafNode
}

var maxCapacity = 1000000

func NewLeafNode() *LeafNode {
	return &LeafNode{
		key:   make([][]byte, 0, maxBaseKeys+1),
		value: make([][]byte, 0, maxBaseKeys+1),
		next:  nil,
	}
}

func (l *LeafNode) Put(key []byte, value []byte) error {
	length := len(l.key)

	for i := 0; i < len(l.key); i++ {

		comp := bytes.Compare(l.key[i], key)
		if comp == 0 {
			// Key matches exactly
			if bytes.Equal(l.value[i], value) {
				return errors.New("Value already exist")
			}
			l.value[i] = value // Update value (Upsert)
			return nil
		}
		if comp > 0 {
			length = i
			break
		}
	}

	l.key = append(l.key, nil)
	l.value = append(l.value, nil)

	if length < len(l.key)-1 {
		copy(l.key[length+1:], l.key[length:])
		copy(l.value[length+1:], l.value[length:])
	}

	l.key[length] = key
	l.value[length] = value
	return nil
}

func (l *LeafNode) Get(key []byte) ([]byte, bool) {
	for i := 0; i < len(l.key); i++ {

		comp := bytes.Compare(l.key[i], key)
		if comp == 0 {
			return l.value[i], true
		}
		if comp > 0 {
			break
		}
	}
	return nil, false
}

func (l *LeafNode) Delete(key []byte) bool {
	length := len(l.key)

	for i := 0; i < len(l.key); i++ {

		comp := bytes.Compare(l.key[i], key)
		if comp == 0 {
			copy(l.key[i:], l.key[i+1:])
			copy(l.value[i:], l.value[i+1:])
			l.key[length-1] = nil
			l.value[length-1] = nil

			l.key = l.key[:length-1]
			l.value = l.value[:length-1]

			return true
		}
		if comp > 0 {
			break
		}
	}
	return false
}
