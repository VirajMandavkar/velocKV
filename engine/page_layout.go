package engine

// Constants for Sizes
const (
	PageSize   = 4096
	HeaderSize = 32
	SlotSize   = 8
)

// Header Byte Offsets ([0:32])
const (
	OffsetVersion       = 0  // 8 bytes, uint64
	OffsetRightSibling  = 8  // 8 bytes, uint64
	OffsetSlotCount     = 16 // 2 bytes, uint16
	OffsetFreeSpace     = 18 // 2 bytes, uint16
	OffsetDeadBytes     = 20 // 2 bytes, uint16
	OffsetPrefixOffset  = 22 // 2 bytes, uint16
	OffsetPrefixLen     = 24 // 2 bytes, uint16
	OffsetHighKeyOffset = 26 // 2 bytes, uint16
	OffsetHighKeyLen    = 28 // 2 bytes, uint16
	OffsetPadding       = 30 // 2 bytes reserved
)

// Version Word Constants
const (
	LockMask           uint64 = 0x0000000000000001 // Bit 0
	ObsoleteMask       uint64 = 0x0000000000000002 // Bit 1
	VersionCounterMask uint64 = 0xFFFFFFFFFFFFFFFC // Bits 2–63
	VersionStep        uint64 = 0x0000000000000004 // Increments the counter by 1
)

// Slot Field Masks and Shifts
const (
	SlotExternalMask uint64 = 1 << 63
	SlotOffsetMask   uint64 = 0x7FFFFFFF00000000 // Bits 32-62
	SlotSuffixMask   uint64 = 0x00000000FFFF0000 // Bits 16-31
	SlotValueMask    uint64 = 0x000000000000FFFF // Bits 0-15

	SlotOffsetShift = 32
	SlotSuffixShift = 16
)

func PackSlot(offset uint32, isExternal bool, suffixLen, valLen uint16) uint64 {
	var slot uint64

	if isExternal {
		slot |= SlotExternalMask
	}
	slot |= (uint64(offset) & 0x7FFFFFFF) << SlotOffsetShift
	slot |= uint64(suffixLen) << SlotSuffixShift
	slot |= uint64(valLen)

	return slot
}

func UnpackSlot(slot uint64) (offset uint32, isExternal bool, suffixLen, valLen uint16) {
	isExternal = (slot & SlotExternalMask) != 0
	offset = uint32((slot & SlotOffsetMask) >> SlotOffsetShift)
	suffixLen = uint16((slot & SlotSuffixMask) >> SlotSuffixShift)
	valLen = uint16(slot & SlotValueMask)

	return offset, isExternal, suffixLen, valLen
}
