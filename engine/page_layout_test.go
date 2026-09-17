package engine

import "testing"

func TestSlotPacking(t *testing.T) {
	t.Run("Roundtrip Fidelity", func(t *testing.T) {
		origOffset := uint32(1048576)
		origIsExternal := true
		origSuffixLen := uint16(32)
		origValLen := uint16(512)

		packed := PackSlot(origOffset, origIsExternal, origSuffixLen, origValLen)
		offset, isExternal, suffixLen, valLen := UnpackSlot(packed)

		if offset != origOffset {
			t.Errorf("Offset mismatch: expected %d, got %d", origOffset, offset)
		}
		if isExternal != origIsExternal {
			t.Errorf("IsExternal mismatch: expected %t, got %t", origIsExternal, isExternal)
		}
		if suffixLen != origSuffixLen {
			t.Errorf("SuffixLen mismatch: expected %d, got %d", origSuffixLen, suffixLen)
		}
		if valLen != origValLen {
			t.Errorf("ValLen mismatch: expected %d, got %d", origValLen, valLen)
		}
	})

	t.Run("Boundary Condition Zero", func(t *testing.T) {
		packed := PackSlot(0, false, 0, 0)
		offset, isExternal, suffixLen, valLen := UnpackSlot(packed)

		if offset != 0 || isExternal || suffixLen != 0 || valLen != 0 {
			t.Errorf("Zero values mismatch: got offset= %d, ext= %t,suffix=%d, val=%d", offset, isExternal, suffixLen, valLen)
		}
	})

	t.Run("External Flag Isolation", func(t *testing.T) {
		offsetValue := uint32(0x3FFFFFFF)
		packedTrue := PackSlot(offsetValue, true, 0, 0)
		offsetTrue, isExtTrue, _, _ := UnpackSlot(packedTrue)

		if !isExtTrue {
			t.Errorf("Expected external flag to be true")
		}
		if offsetTrue != offsetValue {
			t.Errorf("Setting external flag corrupted the offset field. Expected %d, got %d", offsetValue, offsetTrue)
		}

		packedFalse := PackSlot(offsetValue, false, 0, 0)
		offsetFalse, isExtFalse, _, _ := UnpackSlot(packedFalse)

		if isExtFalse {
			t.Errorf("Expected external flag to be false")
		}
		if offsetFalse != offsetValue {
			t.Errorf("Clearing external flag corrupted the offset field. Expected %d, got %d", offsetValue, offsetFalse)
		}
	})

}

func TestVersionWord(t *testing.T) {
	// Start with a baseline clear version word counter
	var version uint64 = 0x0000000000000010 // Binary ends with ...00010000 (Counter value = 4)

	// 1. Setting LockMask leaves version counter bits unchanged
	t.Run("LockMask Invariant", func(t *testing.T) {
		lockedVersion := version | LockMask

		// Assert lock bit (Bit 0) is set
		if (lockedVersion & LockMask) == 0 {
			t.Errorf("Expected LockMask bit to be set")
		}

		// Assert that extracting the counter bits still matches the original counter
		origCounter := version & VersionCounterMask
		newCounter := lockedVersion & VersionCounterMask
		if newCounter != origCounter {
			t.Errorf("Setting LockMask modified counter bits! Expected %d, got %d", origCounter, newCounter)
		}
	})

	// 2. Adding VersionStep increments the counter by 1 without modifying Bit 0 or Bit 1
	t.Run("VersionStep Invariant", func(t *testing.T) {
		initialCounterValue := (version & VersionCounterMask) >> 2

		nextVersion := version + VersionStep
		newCounterValue := (nextVersion & VersionCounterMask) >> 2

		// Assert counter incremented exactly by 1
		if newCounterValue != initialCounterValue+1 {
			t.Errorf("Expected counter value to be %d, got %d", initialCounterValue+1, newCounterValue)
		}

		// Assert bits 0 and 1 were completely untouched (should still be 0)
		if (nextVersion & LockMask) != 0 {
			t.Errorf("VersionStep unintentionally turned on the Lock bit")
		}
		if (nextVersion & ObsoleteMask) != 0 {
			t.Errorf("VersionStep unintentionally turned on the Obsolete bit")
		}
	})
}
