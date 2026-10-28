// Package source runs self-healing: it probes every channel's stream sources for real and switches
// a channel to a verified backup when its source dies, and back when the primary recovers.
package source

import "aurumtv/internal/hls"

// Health statuses.
const (
	StatusUp      = "up"
	StatusDown    = "down"
	StatusUnknown = "unknown"
)

// Summary of the last probe.
type Summary struct {
	OK         bool    `json:"ok"`
	Stage      string  `json:"stage"`
	Reason     *string `json:"reason"`
	LatencyMs  int64   `json:"latencyMs"`
	Resolution *string `json:"resolution"`
}

// Health of one source. LastProbeCycle is in-memory only (nil = never probed in this run).
type Health struct {
	Status           string   `json:"status"`
	ConsecutiveOks   int      `json:"consecutiveOks"`
	ConsecutiveFails int      `json:"consecutiveFails"`
	LastCheckedAt    *string  `json:"lastCheckedAt"`
	LastOkAt         *string  `json:"lastOkAt"`
	LastProbeCycle   *int     `json:"lastProbeCycle"`
	LastResult       *Summary `json:"lastResult"`
}

func EmptyHealth() Health { return Health{Status: StatusUnknown} }

// Thresholds of the hysteresis.
type Thresholds struct {
	Fail    int
	Recover int
}

// Apply updates health with one probe result (pure). Down needs Fail consecutive failures; a down
// source needs Recover consecutive successes to come back (an unknown one comes up on the first).
func Apply(prev Health, r hls.Result, cycle int, t Thresholds) Health {
	h := prev
	checked := r.CheckedAt
	h.LastCheckedAt = &checked
	h.LastProbeCycle = &cycle
	h.LastResult = summarize(r)
	if r.OK {
		h.ConsecutiveOks = prev.ConsecutiveOks + 1
		h.ConsecutiveFails = 0
		h.LastOkAt = &checked
		if prev.Status != StatusDown || h.ConsecutiveOks >= t.Recover {
			h.Status = StatusUp
		}
	} else {
		h.ConsecutiveFails = prev.ConsecutiveFails + 1
		h.ConsecutiveOks = 0
		if h.ConsecutiveFails >= t.Fail {
			h.Status = StatusDown
		}
	}
	return h
}

// Schedule decides which sources are probed in a cycle.
type Schedule struct {
	BackupEveryCycles    int
	DownBackoffMaxCycles int
}

// Due: the active source every cycle; backups periodically; down sources with exponential backoff (capped).
func Due(h Health, cycle int, isActive bool, s Schedule) bool {
	if isActive || h.LastProbeCycle == nil {
		return true
	}
	since := cycle - *h.LastProbeCycle
	if h.Status == StatusDown {
		backoff := 1 << max(0, min(h.ConsecutiveFails-1, 30))
		return since >= min(backoff, s.DownBackoffMaxCycles)
	}
	return since >= s.BackupEveryCycles
}

func summarize(r hls.Result) *Summary {
	s := &Summary{OK: r.OK, Stage: r.Stage, Reason: r.Reason, LatencyMs: r.LatencyMs}
	if r.Resolution != "" {
		res := r.Resolution
		s.Resolution = &res
	}
	return s
}
