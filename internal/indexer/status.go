package indexer

import (
	"sync"
	"time"
)

type ingestionStatus struct {
	mu                  sync.RWMutex
	consecutiveFailures uint64
	totalFailures       uint64
	lastSuccessfulTick  time.Time
	lastCursorAdvance   time.Time
	cursorLedger        uint32
	hasSuccessfulTick   bool
	hasCursorAdvance    bool
}

type ingestionStatusSnapshot struct {
	consecutiveFailures uint64
	totalFailures       uint64
	lastSuccessfulTick  time.Time
	lastCursorAdvance   time.Time
	cursorLedger        uint32
	hasSuccessfulTick   bool
	hasCursorAdvance    bool
}

func (s *ingestionStatus) recordTick(err error, cursorAdvanced bool, ledger uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.consecutiveFailures++
		s.totalFailures++
		return
	}
	s.consecutiveFailures = 0
	s.lastSuccessfulTick = time.Now()
	s.hasSuccessfulTick = true
	if cursorAdvanced {
		s.lastCursorAdvance = s.lastSuccessfulTick
		s.cursorLedger = ledger
		s.hasCursorAdvance = true
	}
}

func (s *ingestionStatus) snapshot() ingestionStatusSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return ingestionStatusSnapshot{
		consecutiveFailures: s.consecutiveFailures,
		totalFailures:       s.totalFailures,
		lastSuccessfulTick:  s.lastSuccessfulTick,
		lastCursorAdvance:   s.lastCursorAdvance,
		cursorLedger:        s.cursorLedger,
		hasSuccessfulTick:   s.hasSuccessfulTick,
		hasCursorAdvance:    s.hasCursorAdvance,
	}
}
