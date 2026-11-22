package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Warewave-Technology/gaia/internal/capacity"
)

// capacity_short (ADR-007 C13): when the proof capacity's coverage drops below 100 %, a fleet-level event in the
// jobs category says so, with the slots needed: once per drop (a state change of entity capacity from ok to short),
// not at every tick. It is a notice on the overview (FleetView.capacity_short) and a line in the daily report, never
// a VM status: a VM turns WARNING only when its proof actually expires. While the sandbox helper does not answer, the
// capacity is unknown and nothing changes.
const (
	capacityEntity   = "capacity"
	capacityOK       = "ok"
	capacityShort    = "short"
	capacityWatchGap = 5 * time.Minute // how often the scheduler looks
)

// CapacityNotice is the proof capacity when it is short.
type CapacityNotice struct {
	Since       string  `json:"since"`
	Coverage    float64 `json:"coverage"` // capacity / demand, below 1
	Slots       int     `json:"slots"`
	SlotsNeeded int     `json:"slots_needed"`
	Text        string  `json:"text"`
}

type capacityWatch struct {
	mu     sync.Mutex
	at     time.Time       // the last look
	state  string          // ok, short; "" before the first look
	notice *CapacityNotice // when short
}

func capacityText(r capacity.Result) string {
	return fmt.Sprintf("proof capacity covers %.0f %% of what the policies ask: %d slot(s), %d needed", r.Coverage*100, r.Slots, r.SlotsNeeded)
}

// watchCapacity computes the proof capacity at most every capacityWatchGap and records a change between ok and short.
func (s *Server) watchCapacity(ctx context.Context, now time.Time) error {
	s.capWatch.mu.Lock()
	due := now.Sub(s.capWatch.at) >= capacityWatchGap
	s.capWatch.mu.Unlock()
	if !due {
		return nil
	}
	s.refreshSandbox(ctx)
	if c, err := s.sandboxCapacity(); err != nil || c == nil {
		return nil // unknown, not short: the helper's own state says why
	}
	in, err := s.capacityInput(ctx, WhatIf{})
	if err != nil {
		return err
	}
	r := capacity.Compute(in)
	to := capacityOK
	if r.Coverage < 1 {
		to = capacityShort
	}
	last, ok, err := s.db.LatestStateChange(ctx, capacityEntity, 1)
	if err != nil {
		return err
	}
	from, since := "", ""
	if ok {
		from, since = last.To, last.At
	}
	if from != to && !(from == "" && to == capacityOK) {
		if err := s.db.RecordState(ctx, capacityEntity, 1, from, to, capacityText(r), "system"); err != nil {
			return err
		}
		if c, ok, err := s.db.LatestStateChange(ctx, capacityEntity, 1); err != nil {
			return err
		} else if ok {
			since = c.At
		}
	}
	s.capWatch.mu.Lock()
	defer s.capWatch.mu.Unlock()
	s.capWatch.at, s.capWatch.state, s.capWatch.notice = now, to, nil
	if to == capacityShort {
		s.capWatch.notice = &CapacityNotice{Since: since, Coverage: r.Coverage, Slots: r.Slots, SlotsNeeded: r.SlotsNeeded, Text: capacityText(r)}
	}
	return nil
}

// capacityNotice is the overview's notice: nil while the capacity is enough or not known.
func (s *Server) capacityNotice() *CapacityNotice {
	s.capWatch.mu.Lock()
	defer s.capWatch.mu.Unlock()
	if s.capWatch.notice == nil {
		return nil
	}
	n := *s.capWatch.notice
	return &n
}

// ReportCapacity is the daily report's proof capacity: short now or not, and how often it dropped since the
// previous report.
type ReportCapacity struct {
	Short bool            `json:"short"`
	Now   *CapacityNotice `json:"now"` // when short
	Drops int             `json:"drops"`
}

func (s *Server) reportCapacity(ctx context.Context, since string) (ReportCapacity, error) {
	n, err := s.db.CountStateChanges(ctx, capacityEntity, capacityShort, since)
	out := ReportCapacity{Now: s.capacityNotice(), Drops: n}
	out.Short = out.Now != nil
	return out, err
}
