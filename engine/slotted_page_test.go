package engine

import (
	"bytes"
	"testing"
)

func TestSlottedPage_Compaction(t *testing.T) {
	var page SlottedPage
	var scratch [PageSize]byte

	// 1. Initialize empty page header
	page.SetSlotCount(3)
	page.SetFreeSpace(3000)
	page.SetDeadBytes(50)

	// Record 0 (Live Inline): suffix="key1", val="val1"
	rec0Heap := uint32(PageSize - 8)
	copy(page.data[rec0Heap:], []byte("key1val1"))
	slot0 := PackSlot(rec0Heap, false, 4, 4)
	WriteSlot(&page.data, HeaderSize, slot0)

	// Record 1 (Tombstone): valLen = 0
	rec1Heap := uint32(PageSize - 16)
	copy(page.data[rec1Heap:], []byte("deadkey!"))
	slot1 := PackSlot(rec1Heap, false, 8, 0)
	WriteSlot(&page.data, HeaderSize+SlotSize, slot1)

	// Record 2 (Live Inline): suffix="k2", val="v2"
	rec2Heap := uint32(PageSize - 20)
	copy(page.data[rec2Heap:], []byte("k2v2"))
	slot2 := PackSlot(rec2Heap, false, 2, 2)
	WriteSlot(&page.data, HeaderSize+(2*SlotSize), slot2)

	// 2. Lock & Compact
	v, ok := page.TryLock()
	if !ok {
		t.Fatalf("expected lock acquisition to succeed")
	}

	page.Compact(&scratch)
	page.PublishScratch(&scratch, v)

	// 3. Verify Header Post-Compaction
	if page.GetSlotCount() != 2 {
		t.Fatalf("expected slot count 2, got %d", page.GetSlotCount())
	}
	if page.GetDeadBytes() != 0 {
		t.Fatalf("expected 0 dead bytes, got %d", page.GetDeadBytes())
	}

	// 4. Verify Payloads
	// Slot 0 (Old rec 0)
	off0, _, sufLen0, valLen0 := UnpackSlot(page.GetSlot(0))
	if sufLen0 != 4 || valLen0 != 4 {
		t.Errorf("slot 0 length mismatch: suf=%d, val=%d", sufLen0, valLen0)
	}
	if !bytes.Equal(page.data[off0:off0+8], []byte("key1val1")) {
		t.Errorf("slot 0 payload corrupted: got %s", page.data[off0:off0+8])
	}

	// Slot 1 (Old rec 2)
	off1, _, sufLen1, valLen1 := UnpackSlot(page.GetSlot(1))
	if sufLen1 != 2 || valLen1 != 2 {
		t.Errorf("slot 1 length mismatch: suf=%d, val=%d", sufLen1, valLen1)
	}
	if !bytes.Equal(page.data[off1:off1+4], []byte("k2v2")) {
		t.Errorf("slot 1 payload corrupted: got %s", page.data[off1:off1+4])
	}
}

func TestSlottedPage_Locking(t *testing.T) {
	var page SlottedPage

	v, ok := page.TryLock()
	if !ok {
		t.Fatalf("first TryLock failed")
	}

	// Concurrent lock attempt should fail
	_, ok2 := page.TryLock()
	if ok2 {
		t.Fatalf("second TryLock succeeded while lock was held")
	}

	// Release
	page.Unlock(v)

	// Verify incremented version counter and cleared lock mask
	cur := *page.versionPtr()
	if cur&LockMask != 0 {
		t.Errorf("lock bit still set after unlock")
	}
	if cur != v+VersionStep {
		t.Errorf("expected version %d, got %d", v+VersionStep, cur)
	}
}

func TestSlottedPage_InsertAndSeek(t *testing.T) {
	var page SlottedPage

	page.SetSlotCount(0)
	page.SetFreeSpace(PageSize - HeaderSize)
	page.SetDeadBytes(0)

	records := []struct {
		suffix string
		value  string
	}{
		{"cherry", "val_cherry"},
		{"apple", "val_apple"},
		{"banana", "val_banana"},
		{"date", "val_date"},
	}

	for _, r := range records {
		ok := page.InsertRecord([]byte(r.suffix), []byte(r.value), false, 0)
		if !ok {
			t.Fatalf("Falied to insert key: %s", r.suffix)
		}
	}

	if page.GetSlotCount() != 4 {
		t.Fatalf("Expected 4 slots, got %d", page.GetSlotCount())
	}

	expectedOrder := []string{"apple", "banana", "cherry", "date"}

	for i, expKey := range expectedOrder {
		idx, found := page.SeekSlot([]byte(expKey))
		if !found {
			t.Fatalf("Expected key %s to be found", expKey)
		}

		if int(idx) != i {
			t.Fatalf("key %s at wrong index: expected %d, got %d", expKey, i, idx)
		}

		rawSlot := page.GetSlot(idx)
		offset, isExt, sufLen, valLen := UnpackSlot(rawSlot)
		if isExt {
			t.Errorf("key %s marked external unexpectedly", expKey)
		}

		gotSuffix := string(page.data[offset : offset+uint32(sufLen)])
		gotVal := string(page.data[offset+uint32(sufLen) : offset+uint32(sufLen)+uint32(valLen)])

		if gotSuffix != expKey {
			t.Errorf("suffix mismatch at slot %d: expected %s, got %s", i, expKey, gotSuffix)
		}
		if gotVal != "val_"+expKey {
			t.Errorf("value mismatch for key %s: got %s", expKey, gotVal)
		}
	}

	missingIdx, found := page.SeekSlot([]byte("apricot"))
	if found {
		t.Fatalf("expected 'apricot' to be missing")
	}
	if missingIdx != 1 {
		t.Fatalf("expected insertion index 1 for 'apricot', got %d", missingIdx)
	}
}

func TestSlottedPage_MutationsAndPrefix(t *testing.T) {
	var page SlottedPage

	// 1. Initialize Page with a Base Prefix: "/registry/pods/"
	page.SetSlotCount(0)
	page.SetFreeSpace(PageSize - HeaderSize)
	page.SetDeadBytes(0)

	basePrefix := []byte("/registry/pods/")
	pfxLen := uint16(len(basePrefix))
	pfxOffset := uint16(PageSize - pfxLen)

	copy(page.data[pfxOffset:], basePrefix)
	page.SetPrefixOffset(pfxOffset)
	page.SetPrefixLen(pfxLen)
	page.SetFreeSpace(page.GetFreeSpace() - pfxLen)

	// 2. Test SplitKey
	suf, matches := page.SplitKey([]byte("/registry/pods/nginx"))
	if !matches || string(suf) != "nginx" {
		t.Fatalf("SplitKey failed: matches=%v, suffix=%s", matches, string(suf))
	}

	_, mismatch := page.SplitKey([]byte("/registry/services/redis"))
	if mismatch {
		t.Fatalf("expected prefix mismatch for non-matching key")
	}

	// 3. Insert an inline record: key suffix "nginx", value "v1_medium_val"
	ok := page.InsertRecord([]byte("nginx"), []byte("v1_medium_val"), false, 0)
	if !ok {
		t.Fatalf("failed to insert initial record")
	}

	// Verify Full Key Assembly
	assembled, ok := page.AssembleFullKey(0, nil)
	if !ok || string(assembled) != "/registry/pods/nginx" {
		t.Fatalf("AssembleFullKey failed: got %s", string(assembled))
	}

	// 4. Test Case 1: In-Place Update (Shrink value: "v1_medium_val" -> "v2_short")
	// Old val length = 13 ("v1_medium_val"), New val length = 8 ("v2_short") -> 5 slack bytes
	ok = page.UpdateRecord([]byte("nginx"), []byte("v2_short"), false, 0)
	if !ok {
		t.Fatalf("in-place UpdateRecord failed")
	}

	if page.GetDeadBytes() != 5 {
		t.Fatalf("expected 5 dead bytes from slack space, got %d", page.GetDeadBytes())
	}

	rawSlot := page.GetSlot(0)
	off, _, sLen, vLen := UnpackSlot(rawSlot)
	valOffset := off + uint32(sLen)
	if string(page.data[valOffset:valOffset+uint32(vLen)]) != "v2_short" {
		t.Fatalf("in-place value update content mismatch: got %s", page.data[valOffset:valOffset+uint32(vLen)])
	}

	// 5. Test Case 2: Out-of-Place Update (Grow value: "v2_short" -> "v3_substantially_longer_payload")
	// Old payload was suffix (5) + val (8) = 13.
	// Previous dead bytes = 5. After out-of-place append, dead bytes should jump to 5 + 13 = 18.
	longVal := []byte("v3_substantially_longer_payload")
	ok = page.UpdateRecord([]byte("nginx"), longVal, false, 0)
	if !ok {
		t.Fatalf("out-of-place UpdateRecord failed")
	}

	if page.GetDeadBytes() != 18 {
		t.Fatalf("expected 18 dead bytes after out-of-place update, got %d", page.GetDeadBytes())
	}

	rawSlot = page.GetSlot(0)
	newOff, _, sLen, vLen := UnpackSlot(rawSlot)
	if newOff >= off {
		t.Fatalf("expected new heap offset to move lower into page, but off=%d, newOff=%d", off, newOff)
	}
	valOffset = newOff + uint32(sLen)
	if string(page.data[valOffset:valOffset+uint32(vLen)]) != string(longVal) {
		t.Fatalf("out-of-place value content mismatch: got %s", page.data[valOffset:valOffset+uint32(vLen)])
	}

	// 6. Test Tombstone Deletion
	ok = page.DeleteRecord([]byte("nginx"))
	if !ok {
		t.Fatalf("DeleteRecord failed")
	}

	// The payload was suffix (5) + longVal (31) = 36.
	// Dead bytes should now be 18 + 36 = 54.
	if page.GetDeadBytes() != 54 {
		t.Fatalf("expected 54 dead bytes after delete, got %d", page.GetDeadBytes())
	}

	// Verify slot is tombstoned (valLen == 0)
	rawSlot = page.GetSlot(0)
	_, _, _, vLen = UnpackSlot(rawSlot)
	if vLen != 0 {
		t.Fatalf("expected valLen = 0 for tombstone, got %d", vLen)
	}

	// Double delete should fail
	if page.DeleteRecord([]byte("nginx")) {
		t.Fatalf("expected redundant delete to return false")
	}
}

func TestSlottedPage_OLCReads(t *testing.T) {
	var page SlottedPage

	// 1. Setup page with a base prefix
	page.SetSlotCount(0)
	page.SetFreeSpace(PageSize - HeaderSize)
	basePrefix := []byte("/api/v1/")
	pfxLen := uint16(len(basePrefix))
	pfxOffset := uint16(PageSize - pfxLen)

	copy(page.data[pfxOffset:], basePrefix)
	page.SetPrefixOffset(pfxOffset)
	page.SetPrefixLen(pfxLen)
	page.SetFreeSpace(page.GetFreeSpace() - pfxLen)

	// 2. Insert a record
	ok := page.InsertRecord([]byte("users"), []byte("json_payload"), false, 0)
	if !ok {
		t.Fatalf("failed to insert")
	}

	// 3. Test Successful Get
	val, isExt, found := page.Get([]byte("/api/v1/users"))
	if !found || isExt || string(val) != "json_payload" {
		t.Fatalf("Get failed: found=%v, isExt=%v, val=%s", found, isExt, string(val))
	}

	// 4. Test Missing Key (prefix match, suffix missing)
	_, _, found = page.Get([]byte("/api/v1/roles"))
	if found {
		t.Fatalf("expected roles to be missing")
	}

	// 5. Test Missing Key (prefix mismatch)
	_, _, found = page.Get([]byte("/api/v2/users"))
	if found {
		t.Fatalf("expected v2 users to be missing due to prefix mismatch")
	}

	// 6. Test Tombstone Rejection
	page.DeleteRecord([]byte("users"))
	_, _, found = page.Get([]byte("/api/v1/users"))
	if found {
		t.Fatalf("expected tombstoned key to return false")
	}
}
