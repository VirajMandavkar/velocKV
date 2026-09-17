package engine

import (
	"math"
	"sync"
	"sync/atomic"
)

const (
	MaxThreads                = 128
	InactiveEpoch      uint64 = math.MaxUint64
	DefaultBinCapacity        = 64
)

type PaddedSlot struct {
	epoch   atomic.Uint64
	padding [56]byte
}

type EBRManager struct {
	globalEpoch atomic.Uint64
	slots       [MaxThreads]PaddedSlot
	retireBins  [3][]uint64
	binMu       sync.Mutex
}

func NewEBRManager() *EBRManager {
	mgr := &EBRManager{}

	mgr.globalEpoch.Store(0)

	for i := 0; i < MaxThreads; i++ {
		mgr.slots[i].epoch.Store(InactiveEpoch)
	}

	for i := 0; i < 3; i++ {
		mgr.retireBins[i] = make([]uint64, 0, 16)
	}

	return mgr
}

func (e *EBRManager) Exit(slotID int) {
	e.slots[slotID].epoch.Store(InactiveEpoch)
}

func (e *EBRManager) Enter(slotID int) uint64 {
	for {
		curr := e.globalEpoch.Load()
		e.slots[slotID].epoch.Store(curr)

		if e.globalEpoch.Load() == curr {
			return curr
		}
	}
}

func (e *EBRManager) Retire(handle uint64) {
	curr := e.globalEpoch.Load()

	idx := curr % 3

	e.binMu.Lock()
	e.retireBins[idx] = append(e.retireBins[idx], handle)
	e.binMu.Unlock()
}

func (e *EBRManager) AdvanceAndReclaim() []uint64 {
	curr := e.globalEpoch.Load()

	for i := 0; i < MaxThreads; i++ {
		sEpoch := e.slots[i].epoch.Load()

		if sEpoch != InactiveEpoch && sEpoch < curr {
			return nil
		}
	}

	e.globalEpoch.Add(1)
	reclaimIdx := (curr + 2) % 3

	var reclaimed []uint64

	e.binMu.Lock()
	reclaimed = e.retireBins[reclaimIdx]
	e.retireBins[reclaimIdx] = make([]uint64, 0, DefaultBinCapacity)
	e.binMu.Unlock()

	return reclaimed
}
