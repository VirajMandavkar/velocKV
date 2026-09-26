package engine

import (
	"bytes"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const MaxInternalKeys = 64

// InternalNode routes searches to the correct child node.
type InternalNode struct {
	version  uint64        // OLC version word
	keys     [][]byte      // N pivot keys
	children []interface{} // N+1 children (can be *InternalNode or *SlottedPage)
	next     *InternalNode //Pointer to the right internal node
}

func (n *InternalNode) WriteLock() {
	for {
		v := atomic.LoadUint64(&n.version)
		if v%2 == 0 {
			if atomic.CompareAndSwapUint64(&n.version, v, v+1) {
				return
			}
		}
		runtime.Gosched()
	}
}

func (n *InternalNode) WriteUnlock() {
	atomic.AddUint64(&n.version, 1)
}

func (in *InternalNode) Route(key []byte) int {
	// Snapshot the slice header. If a writer replaces in.keys with a new slice,
	// this lock-free reader safely continues on the old backing array.
	keys := in.keys
	return sort.Search(len(keys), func(i int) bool {
		return bytes.Compare(key, keys[i]) < 0
	})
}

func (n *InternalNode) ReadLockOrSpin() uint64 {
	for {
		v := atomic.LoadUint64(&n.version)
		if v%2 == 0 {
			return v
		}
		runtime.Gosched()
	}
}

func (n *InternalNode) Validate(initialVersion uint64) bool {
	return atomic.LoadUint64(&n.version) == initialVersion
}

// KVPair represents a result from a range scan
type KVPair struct {
	Key []byte
	Val []byte
}

// Tree represents the full index.
type Tree struct {
	root       interface{}
	mu         sync.RWMutex // Protects global root pointer updates
	ebr        *EBRManager
	nextThread atomic.Uint32
	done       chan struct{} // Signals the GC worker to shut down
}

// NewTree initializes the index and its Epoch-Based Reclamation manager.
func NewTree() *Tree {
	t := &Tree{
		ebr:  NewEBRManager(),
		done: make(chan struct{}),
	}

	// Start the background garbage collector
	go t.reclamationLoop()

	return t
}

// RegisterThread assigns a permanent slot ID for a worker thread to use EBR.
func (t *Tree) RegisterThread() int {
	// Simple round-robin ID assignment up to MaxThreads
	id := t.nextThread.Add(1)
	return int(id % MaxThreads)
}

func (t *Tree) Get(fullkey []byte, threadID int) ([]byte, bool, bool) {

	t.ebr.Enter(threadID)
	defer t.ebr.Exit(threadID)
	t.mu.RLock()
	curr := t.root
	t.mu.RUnlock()

	if curr == nil {
		return nil, false, false
	}

	for {
		switch node := curr.(type) {
		case *InternalNode:
			// OLC Read: spin until the node is unlocked, route, and validate
			for {
				v := node.ReadLockOrSpin()
				children := node.children
				childIdx := node.Route(fullkey)

				// Safety check to prevent panic if a torn read sneaks through
				if childIdx >= len(children) {

					if node.next != nil {
						node = node.next
						continue
					}
					runtime.Gosched()
					continue
				}

				child := children[childIdx]
				if node.Validate(v) {
					curr = child
					break
				}
			}
		case *SlottedPage:
			return node.Get(fullkey)
		default:
			panic("unknown node type in tree")
		}
	}
}

// Put inserts or updates a key-value pair in the tree.
func (t *Tree) Put(key, val []byte, threadID int) {

	t.ebr.Enter(threadID)
	defer t.ebr.Exit(threadID)
	t.mu.RLock()
	root := t.root
	t.mu.RUnlock()

	if root == nil {
		t.mu.Lock()
		if t.root == nil { // Double-check under write lock
			rootPage := &SlottedPage{}
			rootPage.SetSlotCount(0)
			rootPage.SetFreeSpace(PageSize - HeaderSize)
			rootPage.SetPrefixLen(0)
			rootPage.InsertRecord(key, val, false, 0)
			t.root = rootPage
		}
		t.mu.Unlock()
		return
	}

	pivot, newChild := putRecursive(root, key, val)
	if newChild != nil {
		t.mu.Lock()
		// Lock the tree to safely swap the root pointer after a structural split
		newRoot := &InternalNode{
			keys:     [][]byte{pivot},
			children: []interface{}{t.root, newChild},
			next:     nil,
		}
		t.root = newRoot
		t.mu.Unlock()
	}
}

// putRecursive descends the tree, applies the mutation to the leaf,
// and propagates splits back up the call stack.
func putRecursive(node interface{}, key, val []byte) (pivot []byte, rightNode interface{}) {
	switch n := node.(type) {
	case *InternalNode:
		// 1. OLC Read: Safely acquire the child pointer
		var child interface{}
		for {
			v := n.ReadLockOrSpin()
			children := n.children
			idx := n.Route(key)

			if idx >= len(children) {
				if n.next != nil {
					n = n.next
					continue
				}
				runtime.Gosched()
				continue
			}

			child = children[idx]
			if n.Validate(v) {
				break
			}
		}

		// 2. Descend
		childPivot, childRight := putRecursive(child, key, val)

		// 3. Handle splits coming back up
		if childRight != nil {
			n.WriteLock() // Secure internal node before mutating
			defer n.WriteUnlock()

			// CRITICAL: Recalculate the index because the node may have changed!
			insertIdx := n.Route(childPivot)

			n.keys = insertBytes(n.keys, insertIdx, childPivot)
			n.children = insertChild(n.children, insertIdx+1, childRight)

			if len(n.keys) > MaxInternalKeys {
				mid := len(n.keys) / 2
				upPivot := n.keys[mid]

				rightInternal := &InternalNode{
					keys:     append([][]byte(nil), n.keys[mid+1:]...),
					children: append([]interface{}(nil), n.children[mid+1:]...),
					next:     n.next,
				}

				// COW for the left side to protect concurrent lock-free readers
				newKeys := make([][]byte, mid)
				copy(newKeys, n.keys[:mid])
				n.keys = newKeys

				newChildren := make([]interface{}, mid+1)
				copy(newChildren, n.children[:mid+1])
				n.children = newChildren

				n.next = rightInternal

				return upPivot, rightInternal
			}
		}
		return nil, nil

	case *SlottedPage:

		var lockedNode *SlottedPage = n
		for {
			// Defend against the nil void!
			if lockedNode == nil {
				return nil, nil
			}

			if !lockedNode.WriteLock() {
				lockedNode = lockedNode.GetRightSibling()
				continue
			}

			if lockedNode.GetRightSibling() != nil && lockedNode.GetSlotCount() > 0 {
				pivotKey, ok := lockedNode.AssembleFullKey(lockedNode.GetSlotCount()-1, nil)
				if ok && bytes.Compare(key, pivotKey) > 0 {
					lockedNode.WriteUnlock()
					lockedNode = lockedNode.GetRightSibling()
					continue
				}
			}
			break
		}

		defer lockedNode.WriteUnlock()

		suffix, match := lockedNode.SplitKey(key)
		if match {
			// Try an in-place update first if the key is already present
			if lockedNode.UpdateRecord(suffix, val, false, 0) {
				return nil, nil
			}

			// Proactively dry-run check space capacity before applying insertion state changes
			sufLen := uint16(len(suffix))
			valLen := uint16(len(val))
			if EvaluateCapacity(lockedNode.GetSlotCount(), (PageSize-HeaderSize)-lockedNode.GetFreeSpace()-lockedNode.GetDeadBytes(), 0, sufLen, valLen) {
				if lockedNode.InsertRecord(suffix, val, false, 0) {
					return nil, nil
				}
			}
		}

		// Either prefix mismatch or insufficient capacity space: perform structural leaf split
		leafPivot, rightLeaf := lockedNode.Split()

		// Handle degraded or heavily deleted pages that cannot split further
		if rightLeaf == nil {
			insertIntoLeaf(lockedNode, key, val)
			return nil, nil
		}

		// Route the record to its corresponding child boundary
		cmp := bytes.Compare(key, leafPivot)
		if cmp < 0 {
			if !insertIntoLeaf(lockedNode, key, val) {
				// Fallback: If left page cannot accommodate due to prefix mismatch, pass right
				insertIntoLeaf(rightLeaf, key, val)
			}
		} else {
			insertIntoLeaf(rightLeaf, key, val)
		}

		return leafPivot, rightLeaf
	default:
		panic("unknown node type")
	}
}

func deleteRecursive(node interface{}, key []byte, t *Tree) (bool, interface{}) {
	switch n := node.(type) {
	case *InternalNode:
		//1. Lock-Free Descent
		currNode := n
		var child interface{}
		for {
			v := currNode.ReadLockOrSpin()
			children := currNode.children
			idx := currNode.Route(key)

			if idx >= len(children) {
				if currNode.next != nil {
					currNode = currNode.next
					continue
				}
				runtime.Gosched()
				continue
			}
			if currNode.Validate(v) {
				child = children[idx]
				break
			}
		}
		// 2. Descend to the next level
		deleted, obsoleteChild := deleteRecursive(child, key, t)

		// 3. The Ascent: A child died. We must remove its highway sign.
		if obsoleteChild != nil {
			currNode.WriteLock()

			// Finding exactly which child died
			targetIdx := -1
			for i, c := range currNode.children {
				if c == obsoleteChild {
					targetIdx = i
					break
				}
			}

			if targetIdx != -1 {
				// Copy-On-Write deletion to protect concurrent readers
				newChildren := make([]interface{}, 0, len(currNode.children)-1)
				newChildren = append(newChildren, currNode.children[:targetIdx]...)
				newChildren = append(newChildren, currNode.children[targetIdx+1:]...)

				// We also have to remove the corresponding routing key.
				// If targetIdx is 0, we drop the first key. Otherwise, we drop targetIdx-1.
				keyDropIdx := targetIdx
				if keyDropIdx > 0 {
					keyDropIdx--
				}

				newKeys := make([][]byte, 0, len(currNode.keys)-1)
				if len(currNode.keys) > 0 {
					newKeys = append(newKeys, currNode.keys[:keyDropIdx]...)
					newKeys = append(newKeys, currNode.keys[keyDropIdx+1:]...)
				}
				currNode.children = newChildren
				currNode.keys = newKeys
			}
			currNode.WriteUnlock()
		}
		return deleted, nil

	case *SlottedPage:
		// 1. Lock the page (with the detour sign!)
		var lockedNode *SlottedPage = n
		for {
			if lockedNode == nil {
				return false, nil
			}
			if !lockedNode.WriteLock() {
				lockedNode = lockedNode.GetRightSibling()
				continue
			}
			if lockedNode.GetRightSibling() != nil && lockedNode.GetSlotCount() > 0 {
				pivotKey, ok := lockedNode.AssembleFullKey(lockedNode.GetSlotCount()-1, nil)
				if ok && bytes.Compare(key, pivotKey) > 0 {
					lockedNode.WriteUnlock()
					lockedNode = lockedNode.GetRightSibling()
					continue
				}
			}
			break
		}
		defer lockedNode.WriteUnlock()

		// 2. Perform the actual deletion
		suffix, match := lockedNode.SplitKey(key)
		if !match {
			return false, nil
		}

		deleted := lockedNode.DeleteRecord(suffix)

		// 3. Consolidation Check

		if lockedNode.GetFreeSpace()+lockedNode.GetDeadBytes() > PageSize/2 {
			right := lockedNode.GetRightSibling()
			if right != nil {
				merged, handle := lockedNode.MergeRight(right)
				if merged {
					// Tell the background GC to free the memory eventually
					t.ebr.Retire(handle)
					// Signal the parent InternalNode to delete the pointer right now!
					return deleted, right
				}
			}
		}
		return deleted, nil
	default:
		panic("unknown node type")
	}

}

func insertIntoLeaf(p *SlottedPage, key, val []byte) bool {
	suffix, match := p.SplitKey(key)
	if !match {
		if p.GetSlotCount() == 0 {
			p.SetPrefixLen(0)
			return p.InsertRecord(key, val, false, 0)
		}

		return false

	}
	return p.InsertRecord(suffix, val, false, 0)
}

// Copy-On-Write slice insertions to prevent concurrent bounds panics
func insertBytes(slice [][]byte, idx int, val []byte) [][]byte {
	newSlice := make([][]byte, len(slice)+1)
	copy(newSlice[:idx], slice[:idx])
	newSlice[idx] = val
	copy(newSlice[idx+1:], slice[idx:])
	return newSlice
}

func insertChild(slice []interface{}, idx int, val interface{}) []interface{} {
	newSlice := make([]interface{}, len(slice)+1)
	copy(newSlice[:idx], slice[:idx])
	newSlice[idx] = val
	copy(newSlice[idx+1:], slice[idx:])
	return newSlice
}

func (t *Tree) Scan(startKey, endKey []byte, threadID int) []KVPair {
	t.ebr.Enter(threadID)
	defer t.ebr.Exit(threadID)

	t.mu.RLock()
	curr := t.root
	t.mu.RUnlock()

	if curr == nil {
		return nil
	}

	// 1. Route down to the first leaf with OLC validation
	for {
		if node, ok := curr.(*InternalNode); ok {
			for {
				v := node.ReadLockOrSpin()
				children := node.children
				idx := node.Route(startKey)

				if idx >= len(children) {
					if node.next != nil {
						node = node.next
						continue
					}
					runtime.Gosched()
					continue
				}

				child := children[idx]
				if node.Validate(v) {
					curr = child
					break
				}
			}
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
				continue
			}

			fullKey, _ := leaf.AssembleFullKey(i, nil)

			if bytes.Compare(fullKey, startKey) < 0 {
				continue
			}
			if bytes.Compare(fullKey, endKey) >= 0 {
				return results
			}

			valOffset := offset + uint32(sufLen)
			var val []byte
			if isExt {
				val = leaf.data[valOffset : valOffset+8]
			} else {
				val = make([]byte, valLen)
				copy(val, leaf.data[valOffset:valOffset+uint32(valLen)])
			}

			keyCopy := make([]byte, len(fullKey))
			copy(keyCopy, fullKey)

			results = append(results, KVPair{Key: keyCopy, Val: val})
		}

		leaf = leaf.GetRightSibling()
	}

	return results
}

// Delete logically removes a key from the tree by placing a tombstone.
// It returns true if the key was found and deleted, false otherwise.
func (t *Tree) Delete(fullkey []byte, threadID int) bool {
	t.ebr.Enter(threadID)
	defer t.ebr.Exit(threadID)
	t.mu.RLock()
	curr := t.root
	t.mu.RUnlock()

	if curr == nil {
		return false
	}

	deleted, _ := deleteRecursive(curr, fullkey, t)
	return deleted
}

// reclamationLoop runs in the background and physically frees memory
// once all readers have exited the epoch in which it was retired.
func (t *Tree) reclamationLoop() {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-t.done:
			return
		case <-ticker.C:
			reclaimed := t.ebr.AdvanceAndReclaim()
			if len(reclaimed) > 0 {
				// In a C/C++ or mmap-backed Go engine, you would physically
				// free() these pointers or return them to a free-list here.
				// For now, we let the Go GC collect them since no threads
				// hold references to them anymore.
				_ = reclaimed
			}
		}
	}
}

// Close gracefully shuts down the tree and its background workers.
func (t *Tree) Close() {
	close(t.done)
}
