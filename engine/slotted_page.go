package engine

import (
	"bytes"
	"encoding/binary"
	"math"
	"runtime"
	"sync/atomic"
	"unsafe"
)

// SlottedPage represents a fixed 4KB block of memory.
// It avoids GC overhead by keeping all data within a flat byte array.
type SlottedPage struct {
	// version uint64 // OLC version word
	data [PageSize]byte
	next *SlottedPage
}

// versionPtr returns an unsafe pointer to the first 8 bytes of the page,
// which act as the 64-bit OLC (Optimistic Lock Coupling) version word.
func (p *SlottedPage) versionPtr() *uint64 {
	return (*uint64)(unsafe.Pointer(&p.data[0]))
}

// EvaluateCapacity performs a dry-run check to see if a new record (and potential
// prefix shrinkage penalty) will fit within the physical 4096-byte limit.
func EvaluateCapacity(existingRecords uint16, activePayloadBytes uint16, deltaPrefixLen uint16, newSuffixLen uint16, newValLen uint16) bool {
	header := uint16(HeaderSize)
	slots := (existingRecords + 1) * SlotSize
	penalty := existingRecords * deltaPrefixLen // Space needed if prefix shrinks and existing suffixes expand
	totalRequired := header + slots + activePayloadBytes + penalty + newSuffixLen + newValLen

	return totalRequired <= PageSize
}

// Compact performs an out-of-place defragmentation into a thread-local scratch buffer.
// It purges tombstones (valLen == 0), tightly packs surviving payloads at the bottom of the heap,
// and resets dead bytes to zero without interrupting concurrent readers on the live page.
func (p *SlottedPage) Compact(scratch *[PageSize]byte) {
	nextSlotOffset := uint32(HeaderSize)
	nextHeapOffset := uint32(PageSize)

	// 1. Preserve the prefix in the scratch buffer
	pfxLen := p.GetPrefixLen()
	if pfxLen > 0 {
		pfxOff := p.GetPrefixOffset()
		nextHeapOffset -= uint32(pfxLen)
		copy(scratch[nextHeapOffset:], p.data[pfxOff:pfxOff+pfxLen])

		binary.LittleEndian.PutUint16(scratch[OffsetPrefixOffset:OffsetPrefixOffset+2], uint16(nextHeapOffset))
		binary.LittleEndian.PutUint16(scratch[OffsetPrefixLen:OffsetPrefixLen+2], pfxLen)
	}

	slotCount := p.GetSlotCount()
	var liveCount uint16 = 0

	// 2. Compact remaining live slots
	for i := uint16(0); i < slotCount; i++ {
		rawSlot := p.GetSlot(i)

		offset, isExt, sufLen, valLen := UnpackSlot(rawSlot)
		if valLen == 0 {
			continue
		}

		var payloadLen uint32
		if isExt {
			payloadLen = uint32(sufLen) + 8 // suffix + 8-byte arena handle
		} else {
			payloadLen = uint32(sufLen) + uint32(valLen) // suffix + value
		}
		nextHeapOffset -= payloadLen
		copy(scratch[nextHeapOffset:], p.data[offset:offset+payloadLen])

		newRawSlot := PackSlot(nextHeapOffset, isExt, sufLen, valLen)
		WriteSlot(scratch, nextSlotOffset, newRawSlot)

		nextSlotOffset += SlotSize
		liveCount++
	}

	freeSpaceRemaining := uint16(nextHeapOffset - nextSlotOffset)

	binary.LittleEndian.PutUint16(scratch[OffsetSlotCount:OffsetSlotCount+2], liveCount)
	binary.LittleEndian.PutUint16(scratch[OffsetFreeSpace:OffsetFreeSpace+2], freeSpaceRemaining)
	binary.LittleEndian.PutUint16(scratch[OffsetDeadBytes:OffsetDeadBytes+2], 0)
}

// -----------------------------------------------------------------------------
// Header Accessors (Byte-level reading/writing to avoid struct mapping)
// -----------------------------------------------------------------------------

func (p *SlottedPage) GetSlotCount() uint16 {
	return binary.LittleEndian.Uint16(p.data[OffsetSlotCount : OffsetSlotCount+2])
}

func (p *SlottedPage) SetSlotCount(count uint16) {
	binary.LittleEndian.PutUint16(p.data[OffsetSlotCount:OffsetSlotCount+2], count)
}

func (p *SlottedPage) GetFreeSpace() uint16 {
	return binary.LittleEndian.Uint16(p.data[OffsetFreeSpace : OffsetFreeSpace+2])
}

func (p *SlottedPage) SetFreeSpace(freeSpace uint16) {
	binary.LittleEndian.PutUint16(p.data[OffsetFreeSpace:OffsetFreeSpace+2], freeSpace)
}

func (p *SlottedPage) GetDeadBytes() uint16 {
	return binary.LittleEndian.Uint16(p.data[OffsetDeadBytes : OffsetDeadBytes+2])
}

func (p *SlottedPage) SetDeadBytes(deadBytes uint16) {
	binary.LittleEndian.PutUint16(p.data[OffsetDeadBytes:OffsetDeadBytes+2], deadBytes)
}

// GetSlot retrieves the 64-bit bit-packed descriptor at a given logical index.
func (p *SlottedPage) GetSlot(idx uint16) uint64 {
	offset := HeaderSize + (int(idx) * SlotSize)
	return binary.LittleEndian.Uint64(p.data[offset : offset+SlotSize])
}

// -----------------------------------------------------------------------------
// Prefix Compression Accessors
// -----------------------------------------------------------------------------

func (p *SlottedPage) GetPrefixOffset() uint16 {
	return binary.LittleEndian.Uint16(p.data[OffsetPrefixOffset : OffsetPrefixOffset+2])
}

func (p *SlottedPage) SetPrefixOffset(offset uint16) {
	binary.LittleEndian.PutUint16(p.data[OffsetPrefixOffset:OffsetPrefixOffset+2], offset)
}

func (p *SlottedPage) GetPrefixLen() uint16 {
	return binary.LittleEndian.Uint16(p.data[OffsetPrefixLen : OffsetPrefixLen+2])
}

func (p *SlottedPage) SetPrefixLen(length uint16) {
	binary.LittleEndian.PutUint16(p.data[OffsetPrefixLen:OffsetPrefixLen+2], length)
}

// GetBasePrefix extracts the shared prefix bytes for all keys in this page.
func (p *SlottedPage) GetBasePrefix() []byte {
	pLen := p.GetPrefixLen()
	if pLen == 0 {
		return nil
	}
	pOff := p.GetPrefixOffset()
	return p.data[pOff : pOff+pLen]
}

// WriteSlot injects a 64-bit slot descriptor directly into a byte buffer.
func WriteSlot(buf *[PageSize]byte, offset uint32, rawSlot uint64) {
	binary.LittleEndian.PutUint64(buf[offset:offset+SlotSize], rawSlot)
}

// -----------------------------------------------------------------------------
// OLC Writer Primitives
// -----------------------------------------------------------------------------

// TryLock attempts to set the Lock bit (Bit 0) using atomic Compare-And-Swap.
// Fails if another writer holds the lock or if the page is marked obsolete.
func (p *SlottedPage) TryLock() (uint64, bool) {
	v := atomic.LoadUint64(p.versionPtr())
	if (v&LockMask) != 0 || (v&ObsoleteMask) != 0 {
		return 0, false
	}
	swapped := atomic.CompareAndSwapUint64(p.versionPtr(), v, v|LockMask)
	if swapped {
		return v, true
	}
	return 0, false
}

// PublishScratch atomically overwrites the live page with compacted scratch data,
// then releases the OLC write latch.
func (p *SlottedPage) PublishScratch(scratch *[PageSize]byte) {
	copy(p.data[8:], scratch[8:]) // Skip overwriting the 8-byte version word
	p.WriteUnlock()
}

// -----------------------------------------------------------------------------
// Core Page Mutations & Lookups
// -----------------------------------------------------------------------------

// SeekSlot performs a binary search over the sorted slot array for a specific suffix.
// Returns the exact slot index if found, or the insertion index if missing.
func (p *SlottedPage) SeekSlot(targetSuffix []byte) (idx uint16, exactMatch bool) {
	count := p.GetSlotCount()
	if count == 0 {
		return 0, false
	}

	var low int = 0
	var high int = int(count) - 1

	for low <= high {
		mid := (low + high) / 2
		rawSlot := p.GetSlot(uint16(mid))
		offset, _, sufLen, _ := UnpackSlot(rawSlot)

		midSuffix := p.data[offset : offset+uint32(sufLen)]
		cmp := bytes.Compare(targetSuffix, midSuffix)

		if cmp == 0 {
			return uint16(mid), true
		} else if cmp < 0 {
			high = mid - 1
		} else {
			low = mid + 1
		}
	}
	return uint16(low), false
}

// InsertRecord handles sorted placement of a new key.
// It checks capacity, carves space at the heap bottom, shifts slots right
// to maintain lexicographical order, and updates headers.
func (p *SlottedPage) InsertRecord(suffix []byte, val []byte, isExt bool, arenaHandle uint64) bool {
	idx, exactMatch := p.SeekSlot(suffix)
	if exactMatch {
		return false
	}

	sufLen := uint16(len(suffix))
	valLen := uint32(len(val))

	var payloadLen uint32
	if isExt {
		payloadLen = uint32(sufLen) + 8
	} else {
		payloadLen = uint32(sufLen) + valLen
	}

	needed := uint16(payloadLen + SlotSize)
	if p.GetFreeSpace() < needed {
		return false
	}

	slotCount := p.GetSlotCount()
	currentHeapBottom := uint32(HeaderSize) + uint32(slotCount)*SlotSize + uint32(p.GetFreeSpace())
	newHeapOffset := currentHeapBottom - payloadLen

	// Write payload
	copy(p.data[newHeapOffset:], suffix)
	if isExt {
		binary.LittleEndian.PutUint64(p.data[newHeapOffset+uint32(sufLen):], arenaHandle)
	} else {
		copy(p.data[newHeapOffset+uint32(sufLen):], val)
	}

	// Shift upper slots to make room
	srcStart := HeaderSize + int(idx)*SlotSize
	srcEnd := HeaderSize + int(slotCount)*SlotSize
	if idx < slotCount {
		copy(p.data[srcStart+SlotSize:srcEnd+SlotSize], p.data[srcStart:srcEnd])
	}

	// Inject new slot descriptor
	newSlot := PackSlot(newHeapOffset, isExt, sufLen, uint16(valLen))
	WriteSlot(&p.data, uint32(srcStart), newSlot)

	p.SetSlotCount(slotCount + 1)
	p.SetFreeSpace(p.GetFreeSpace() - needed)

	return true
}

// DeleteRecord performs an O(1) logical deletion.
// It sets valLen = 0 in the slot descriptor and registers dead bytes,
// avoiding slot shifting that would break concurrent reader indices.
func (p *SlottedPage) DeleteRecord(suffix []byte) bool {
	idx, found := p.SeekSlot(suffix)
	if !found {
		return false
	}

	rawSlot := p.GetSlot(idx)
	offset, isExt, sufLen, valLen := UnpackSlot(rawSlot)

	if valLen == 0 {
		return false
	}

	var payLoadBytes uint32
	if isExt {
		payLoadBytes = uint32(sufLen) + 8
	} else {
		payLoadBytes = uint32(sufLen) + uint32(valLen)
	}

	p.SetDeadBytes(p.GetDeadBytes() + uint16(payLoadBytes))
	newSlot := PackSlot(offset, isExt, sufLen, 0)
	slotByteOffset := HeaderSize + int(idx)*SlotSize
	WriteSlot(&p.data, uint32(slotByteOffset), newSlot)

	return true
}

// UpdateRecord handles both in-place overwrites (if new value is <= old value)
// and out-of-place heap appends (if new value expands).
func (p *SlottedPage) UpdateRecord(suffix []byte, newVal []byte, isExt bool, arenaHandle uint64) bool {
	idx, found := p.SeekSlot(suffix)
	if !found {
		return false
	}

	rawSlot := p.GetSlot(idx)
	offset, oldIsExt, sufLen, oldValLen := UnpackSlot(rawSlot)
	if oldValLen == 0 {
		return false // Can't update a tombstone directly
	}

	newValLen := uint32(len(newVal))

	// Path 1: In-place update (generates slack space)
	if !oldIsExt && !isExt && newValLen <= uint32(oldValLen) {
		valOffset := int(offset) + int(sufLen)
		copy(p.data[valOffset:valOffset+int(newValLen)], newVal)

		slack := uint32(oldValLen) - newValLen
		if slack > 0 {
			p.SetDeadBytes(p.GetDeadBytes() + uint16(slack))
		}

		newSlot := PackSlot(offset, false, sufLen, uint16(newValLen))
		WriteSlot(&p.data, uint32(HeaderSize+(int(idx))*SlotSize), newSlot)
		return true
	}

	// Path 2: Out-of-place update (allocates new heap space, abandons old payload)
	var oldPayloadLen uint32
	if oldIsExt {
		oldPayloadLen = uint32(sufLen) + 8
	} else {
		oldPayloadLen = uint32(sufLen) + uint32(oldValLen)
	}

	var newPayloadLen uint32
	if isExt {
		newPayloadLen = uint32(sufLen) + 8
	} else {
		newPayloadLen = uint32(sufLen) + newValLen
	}

	slotEndOffset := uint32(HeaderSize) + uint32(p.GetSlotCount())*SlotSize
	currentHeapBottom := slotEndOffset + uint32(p.GetFreeSpace())

	if uint32(p.GetFreeSpace()) < newPayloadLen {
		return false
	}

	newHeapOffset := currentHeapBottom - newPayloadLen
	copy(p.data[newHeapOffset:], suffix)

	dataWriteOffset := newHeapOffset + uint32(sufLen)
	if isExt {
		binary.LittleEndian.PutUint64(p.data[dataWriteOffset:], arenaHandle)
	} else {
		copy(p.data[dataWriteOffset:], newVal)
	}

	updatedSlot := PackSlot(newHeapOffset, isExt, sufLen, uint16(newValLen))
	WriteSlot(&p.data, uint32(HeaderSize+int(idx)*SlotSize), updatedSlot)

	p.SetDeadBytes(p.GetDeadBytes() + uint16(oldPayloadLen))
	p.SetFreeSpace(p.GetFreeSpace() - uint16(newPayloadLen))

	return true
}

// AssembleFullKey stitches the header's base prefix and the slot's suffix
// into a complete key for upstream consumption.
func (p *SlottedPage) AssembleFullKey(slotIdx uint16, dst []byte) ([]byte, bool) {
	if slotIdx >= p.GetSlotCount() {
		return nil, false
	}

	rawSlot := p.GetSlot(slotIdx)
	offset, _, sufLen, _ := UnpackSlot(rawSlot)
	pfx := p.GetBasePrefix()
	totalKeyLen := len(pfx) + int(sufLen)

	if cap(dst) < totalKeyLen {
		dst = make([]byte, totalKeyLen)
	} else {
		dst = dst[:totalKeyLen]
	}

	copy(dst[:len(pfx)], pfx)
	copy(dst[len(pfx):], p.data[offset:offset+uint32(sufLen)])

	return dst, true
}

// SplitKey checks if an incoming full key matches the page's base prefix.
// Returns the remaining suffix if it matches, or false if it diverges.
func (p *SlottedPage) SplitKey(fullKey []byte) ([]byte, bool) {
	pfx := p.GetBasePrefix()
	if len(pfx) == 0 {
		return fullKey, true
	}

	if !bytes.HasPrefix(fullKey, pfx) {
		return nil, false
	}

	return fullKey[len(pfx):], true
}

// -----------------------------------------------------------------------------
// OLC Reader Primitives
// -----------------------------------------------------------------------------

// ReadLockOrSpin acquires a snapshot of the version word for readers.
// If a writer holds the lock (Bit 0 is 1), it yields the CPU until the writer finishes.
func (p *SlottedPage) ReadLockOrSpin() uint64 {
	for {
		v := atomic.LoadUint64(p.versionPtr())
		if (v & ObsoleteMask) != 0 {
			return math.MaxUint64
		}

		if v&LockMask == 0 {
			return v
		}
		runtime.Gosched() // Yield to prevent burning CPU cycles in a tight spin loop
	}
}

// Validate confirms the version word hasn't changed since the reader began looking.
// It fails if a writer incremented the version or acquired the lock mid-read.
func (p *SlottedPage) Validate(initialVersion uint64) bool {
	curr := atomic.LoadUint64(p.versionPtr())
	return curr == initialVersion && (curr&(LockMask|ObsoleteMask) == 0)
}

func (p *SlottedPage) GetRecord(fullKey []byte) ([]byte, bool, bool) {
	initialVersion := p.ReadLockOrSpin()

	if initialVersion == math.MaxUint64 {
		return nil, false, false // valid = false triggers a retry/traverse in Get()
	}

	suffix, matchesPrefix := p.SplitKey(fullKey)
	if !matchesPrefix {
		return nil, false, true
	}

	idx, found := p.SeekSlot(suffix)
	if !found {
		return nil, false, true
	}

	rawSlot := p.GetSlot(idx)

	offset, isExt, sufLen, valLen := UnpackSlot(rawSlot)
	if valLen == 0 {
		return nil, false, true
	}

	var resultPayload []byte
	var readOffset int

	if isExt {
		readOffset = int(offset) + int(sufLen)
		resultPayload = make([]byte, 8)
		copy(resultPayload, p.data[readOffset:readOffset+8])
	} else {
		readOffset = int(offset) + int(sufLen)
		resultPayload = make([]byte, valLen)
		copy(resultPayload, p.data[readOffset:readOffset+int(valLen)])
	}

	if !p.Validate(initialVersion) {
		return nil, false, false
	}

	return resultPayload, isExt, true

}

// Get performs a thread-safe point lookup, spinning and retrying automatically
// if concurrent writers invalidate the optimistic read.
// Returns: (payload, isExternal, found)
func (p *SlottedPage) Get(fullKey []byte) ([]byte, bool, bool) {
	curr := p

	for {
		if curr == nil {
			return nil, false, false
		}
		val, isExt, valid := curr.GetRecord(fullKey)

		if valid {

			if val != nil {
				return val, isExt, true
			}

			if curr.GetRightSibling() != nil {
				if curr.GetSlotCount() > 0 {
					pivotKey, ok := curr.AssembleFullKey(curr.GetSlotCount()-1, nil)

					if ok && bytes.Compare(fullKey, pivotKey) > 0 {
						curr = curr.GetRightSibling()
						continue
					}
				}
			}
			return nil, isExt, false
		}

		v := atomic.LoadUint64(curr.versionPtr())
		if (v & ObsoleteMask) != 0 {
			curr = curr.GetRightSibling()
			continue
		}
		// Validation failed (page was modified mid-read). Yield and retry.
		runtime.Gosched()
	}
}

// GetRightSibling returns the page ID or memory pointer of the right sibling.
func (p *SlottedPage) GetRightSibling() *SlottedPage {
	return p.next
}

// SetRightSibling sets the page ID or memory pointer of the right sibling.
func (p *SlottedPage) SetRightSibling(sibling *SlottedPage) {
	p.next = sibling
}

// Split divides the page in half, migrating the upper half of the slots to a new right sibling.
// It returns the pivot key (full key at the median) and the new right SlottedPage.
func (p *SlottedPage) Split() (pivotKey []byte, rightPage *SlottedPage) {
	slotCount := p.GetSlotCount()
	if slotCount < 2 {
		return nil, nil
	}

	mid := slotCount / 2

	// 1. Extract the Pivot Key (Full Key) before we mutate anything
	pivotKey, _ = p.AssembleFullKey(mid, nil)

	// 2. Initialize Right Page
	rightPage = &SlottedPage{}
	rightPage.SetSlotCount(0)
	rightPage.SetFreeSpace(PageSize - HeaderSize)
	rightPage.SetDeadBytes(0)

	// 3. Inherit Base Prefix
	pfx := p.GetBasePrefix()
	if len(pfx) > 0 {
		pfxLen := uint16(len(pfx))
		pfxOffset := uint16(PageSize - pfxLen)
		copy(rightPage.data[pfxOffset:], pfx)
		rightPage.SetPrefixOffset(pfxOffset)
		rightPage.SetPrefixLen(pfxLen)
		rightPage.SetFreeSpace(rightPage.GetFreeSpace() - pfxLen)
	}

	// 4. Migrate Upper Half (Slots from mid to slotCount - 1)
	for i := mid; i < slotCount; i++ {
		rawSlot := p.GetSlot(i)
		offset, isExt, sufLen, valLen := UnpackSlot(rawSlot)
		if valLen == 0 {
			continue
		}
		suffix := p.data[offset : offset+uint32(sufLen)]

		var val []byte
		var arenaHandle uint64

		valOffset := offset + uint32(sufLen)

		if isExt {
			arenaHandle = binary.LittleEndian.Uint64(p.data[valOffset : valOffset+8])
		} else {
			val = p.data[valOffset : valOffset+uint32(valLen)]
		}

		_ = rightPage.InsertRecord(suffix, val, isExt, arenaHandle)
	}

	p.SetSlotCount(mid)

	var scratch [PageSize]byte

	p.Compact(&scratch)

	copy(p.data[8:], scratch[8:])

	// Link the B-Link tree siblings
	rightPage.SetRightSibling(p.GetRightSibling()) // New page points to old right sibling
	p.SetRightSibling(rightPage)                   // Left page points to new right page

	return pivotKey, rightPage

}

// WriteLock atomically sets the version from even to odd.
func (p *SlottedPage) WriteLock() {
	for {
		v := atomic.LoadUint64(p.versionPtr())
		if (v & (LockMask | ObsoleteMask)) == 0 { // If unlocked
			if atomic.CompareAndSwapUint64(p.versionPtr(), v, v|LockMask) {
				return
			}
		}
		// Yield to prevent CPU hogging during contention
		runtime.Gosched()
	}
}

// WriteUnlock releases the lock by making the version even again.
// Readers will see the new version and know the data changed.
func (p *SlottedPage) WriteUnlock() {
	v := atomic.LoadUint64(p.versionPtr())
	newValue := (v + VersionStep) &^ LockMask
	atomic.StoreUint64(p.versionPtr(), newValue)
}

// MarkObsolete flags the page as logically deleted for concurrent readers,
// and releases the write lock simultaneously.
func (p *SlottedPage) MarkObsolete() {
	for {
		v := atomic.LoadUint64(p.versionPtr())
		newV := ((v + VersionStep) &^ LockMask) | ObsoleteMask
		if atomic.CompareAndSwapUint64(p.versionPtr(), v, newV) {
			return
		}
	}
}

// MergeRight attempts to pull all live records from the right sibling into this page.
// The caller must already hold the WriteLock on 'p'.
func (p *SlottedPage) MergeRight(right *SlottedPage) (merged bool, obsoleteHandle uint64) {
	right.WriteLock()

	// 1. Calculate live payloads
	leftLive := (PageSize - HeaderSize) - p.GetFreeSpace() - p.GetDeadBytes()
	rightLive := (PageSize - HeaderSize) - right.GetFreeSpace() - right.GetDeadBytes()

	// 2. Abort if they don't safely fit into a single 4KB page (with a small buffer)
	if leftLive+rightLive > (PageSize - HeaderSize - 128) {
		right.WriteUnlock()
		return false, 0
	}

	// 3. Compact the left page to ensure physical contiguous space is available
	var scratch [PageSize]byte
	p.Compact(&scratch)
	copy(p.data[8:], scratch[8:]) // Skip overwriting the version word

	// 4. Migrate records
	slotCount := right.GetSlotCount()
	for i := uint16(0); i < slotCount; i++ {
		rawSlot := right.GetSlot(i)
		offset, isExt, sufLen, valLen := UnpackSlot(rawSlot)
		if valLen == 0 {
			continue // Skip right page tombstones
		}

		suffix := right.data[offset : offset+uint32(sufLen)]
		fullKey, _ := right.AssembleFullKey(i, suffix)

		valOffset := offset + uint32(sufLen)
		var val []byte
		var arenaHandle uint64

		if isExt {
			arenaHandle = binary.LittleEndian.Uint64(right.data[valOffset : valOffset+8])
		} else {
			val = right.data[valOffset : valOffset+uint32(valLen)]
		}

		// Adjust prefix if necessary to fit the left page's structure
		leftSuffix, match := p.SplitKey(fullKey)
		if !match {
			p.SetPrefixLen(0)
			leftSuffix = fullKey
		}
		p.InsertRecord(leftSuffix, val, isExt, arenaHandle)
	}

	// 5. Update B-Link pointer to skip the right page
	p.SetRightSibling(right.GetRightSibling())

	// 6. Mark Right as obsolete (this also unlocks it)
	right.MarkObsolete()

	// 7. Convert the dead page pointer to a uint64 handle for the EBR worker
	handle := uint64(uintptr(unsafe.Pointer(right)))

	return true, handle
}
