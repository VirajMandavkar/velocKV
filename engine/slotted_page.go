package engine

import (
	"bytes"
	"encoding/binary"
	"sync/atomic"
	"unsafe"
)

type SlottedPage struct {
	data [PageSize]byte
}

func (p *SlottedPage) versionPtr() *uint64 {
	return (*uint64)(unsafe.Pointer(&p.data[0]))
}

func EvaluateCapacity(existingRecords uint16, activePayloadBytes uint16, deltaPrefixLen uint16, newSuffixLen uint16, newValLen uint16) bool {
	header := uint16(HeaderSize)

	slots := (existingRecords + 1) * SlotSize
	penalty := existingRecords * deltaPrefixLen

	totalRequired := header + slots + activePayloadBytes + penalty + newSuffixLen + newValLen

	return totalRequired <= PageSize
}

func (p *SlottedPage) Compact(scratch *[PageSize]byte) {
	nextSlotOffset := uint32(HeaderSize)
	nextHeapOffset := uint32(PageSize)

	slotCount := p.GetSlotCount()
	var liveCount uint16 = 0

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

// GetSlotCount returns the number of active slot descriptors in the page header.
func (p *SlottedPage) GetSlotCount() uint16 {
	return binary.LittleEndian.Uint16(p.data[OffsetSlotCount : OffsetSlotCount+2])
}

// SetSlotCount updates the slot count in the page header.
func (p *SlottedPage) SetSlotCount(count uint16) {
	binary.LittleEndian.PutUint16(p.data[OffsetSlotCount:OffsetSlotCount+2], count)
}

// GetFreeSpace returns the total contiguous byte gap between slots and heap bottom.
func (p *SlottedPage) GetFreeSpace() uint16 {
	return binary.LittleEndian.Uint16(p.data[OffsetFreeSpace : OffsetFreeSpace+2])
}

// SetFreeSpace updates the contiguous unallocated gap in the page header.
func (p *SlottedPage) SetFreeSpace(freeSpace uint16) {
	binary.LittleEndian.PutUint16(p.data[OffsetFreeSpace:OffsetFreeSpace+2], freeSpace)
}

// GetDeadBytes returns the accumulated bytes consumed by deleted/stale records.
func (p *SlottedPage) GetDeadBytes() uint16 {
	return binary.LittleEndian.Uint16(p.data[OffsetDeadBytes : OffsetDeadBytes+2])
}

// SetDeadBytes updates the total dead byte count in the page header.
func (p *SlottedPage) SetDeadBytes(deadBytes uint16) {
	binary.LittleEndian.PutUint16(p.data[OffsetDeadBytes:OffsetDeadBytes+2], deadBytes)
}

// GetSlot retrieves the packed 64-bit slot entry at logical index idx.
func (p *SlottedPage) GetSlot(idx uint16) uint64 {
	offset := HeaderSize + (int(idx) * SlotSize)
	return binary.LittleEndian.Uint64(p.data[offset : offset+SlotSize])
}

// WriteSlot writes a packed 64-bit slot entry into a target buffer at byte offset.
func WriteSlot(buf *[PageSize]byte, offset uint32, rawSlot uint64) {
	binary.LittleEndian.PutUint64(buf[offset:offset+SlotSize], rawSlot)
}

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

func (p *SlottedPage) Unlock(oldVersion uint64) {
	newVersion := oldVersion + VersionStep
	atomic.StoreUint64(p.versionPtr(), newVersion)
}

func (p *SlottedPage) PublishScratch(scratch *[PageSize]byte, oldVersion uint64) {
	copy(p.data[8:], scratch[8:])
	p.Unlock(oldVersion)
}

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

	copy(p.data[newHeapOffset:], suffix)
	if isExt {
		// Write 8-byte arenaHandle after suffix
		binary.LittleEndian.PutUint64(p.data[newHeapOffset+uint32(sufLen):], arenaHandle)
	} else {
		// Copy value bytes after suffix
		copy(p.data[newHeapOffset+uint32(sufLen):], val)
	}

	srcStart := HeaderSize + int(idx)*SlotSize
	srcEnd := HeaderSize + int(slotCount)*SlotSize
	if idx < slotCount {
		copy(p.data[srcStart+SlotSize:srcEnd+SlotSize], p.data[srcStart:srcEnd])
	}

	newSlot := PackSlot(newHeapOffset, isExt, sufLen, uint16(valLen))
	WriteSlot(&p.data, uint32(srcStart), newSlot)

	p.SetSlotCount(slotCount + 1)
	p.SetFreeSpace(p.GetFreeSpace() - needed)

	return true
}
