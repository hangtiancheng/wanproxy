// Package benchsvc is the Go port of src/bench/bench.service.ts.  It
// guards against concurrent runs, resolves a scenario, samples the
// monitor codec counters before/after, and executes the benchmark
// in-process via the bench library.
package benchsvc

import (
	"errors"
	"fmt"
	"sync"

	"github.com/hangtiancheng/wanproxy/srv.go/internal/bench"
	"github.com/hangtiancheng/wanproxy/srv.go/internal/monitor"
	"github.com/hangtiancheng/wanproxy/srv.go/internal/payload"
)

// ErrAlreadyRunning is returned when a benchmark is in flight.
var ErrAlreadyRunning = errors.New("a benchmark is already running")

// Request is the POST /api/bench/run body.  Optional fields use
// pointers so an explicit zero is distinguishable from "absent".
type Request struct {
	Scenario  string  `json:"scenario"`
	Direction *string `json:"direction"`
	Pattern   *string `json:"pattern"`
	Size      *int64  `json:"size"`
	Seed      *int64  `json:"seed"`
}

// BenchStats carries the monitor counter deltas for the run.
type BenchStats struct {
	Proxy     string             `json:"proxy"`
	Codec     string             `json:"codec"`
	AppBytes  int64              `json:"appBytes"`
	WireBytes int64              `json:"wireBytes"`
	Ratio     *float64           `json:"ratio"`
	Raw       monitor.CodecStats `json:"raw"`
}

// BenchResult is the POST /api/bench/run response.  It embeds the full
// scenario object, matching web/src/lib/api.ts.
type BenchResult struct {
	Scenario       monitor.Scenario `json:"scenario"`
	Direction      string           `json:"direction"`
	Pattern        string           `json:"pattern"`
	Size           int64            `json:"size"`
	Seed           int64            `json:"seed"`
	DurationMs     float64          `json:"durationMs"`
	ThroughputMBps float64          `json:"throughputMBps"`
	SHA256         string           `json:"sha256,omitempty"`
	OK             bool             `json:"ok"`
	Error          string           `json:"error,omitempty"`
	Stats          *BenchStats      `json:"stats,omitempty"`
}

// ErrUnknownScenario signals an unrecognized scenario id.
type ErrUnknownScenario struct{ ID string }

func (e *ErrUnknownScenario) Error() string {
	return fmt.Sprintf("unknown scenario %s", e.ID)
}

// Service serializes benchmark runs.
type Service struct {
	mu      sync.Mutex
	running bool
	monitor *monitor.Service
}

// NewService builds the bench service.
func NewService(m *monitor.Service) *Service { return &Service{monitor: m} }

const defaultSize = 16 * 1024 * 1024

// Run executes one benchmark, returning the result (which reports
// transfer failures in-band via OK=false).
func (s *Service) Run(req Request) (*BenchResult, error) {
	scenario, ok := monitor.FindScenario(req.Scenario)
	if !ok {
		return nil, &ErrUnknownScenario{ID: req.Scenario}
	}

	direction := "roundtrip"
	if req.Direction != nil && *req.Direction != "" {
		direction = *req.Direction
	}
	patternStr := "mixed"
	if req.Pattern != nil && *req.Pattern != "" {
		patternStr = *req.Pattern
	}
	size := int64(defaultSize)
	if req.Size != nil {
		size = *req.Size
	}
	seed := int64(42)
	if req.Seed != nil {
		seed = *req.Seed
	}
	pattern, err := payload.ParsePattern(patternStr)
	if err != nil {
		return nil, err
	}

	if !s.begin() {
		return nil, ErrAlreadyRunning
	}
	defer s.end()
	return s.runOnceLocked(scenario, direction, patternStr, pattern, size, seed), nil
}

// begin acquires the single-flight slot shared by Run and
// RunExperiment, so at most one transfer series is ever in flight.
func (s *Service) begin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	return true
}

func (s *Service) end() {
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
}

// Busy reports whether a benchmark or experiment is in flight.
func (s *Service) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// runOnceLocked executes one transfer and samples the monitor codec
// counters around it.  Callers must hold the single-flight slot.
func (s *Service) runOnceLocked(scenario monitor.Scenario, direction, patternStr string, pattern payload.Pattern, size, seed int64) *BenchResult {
	trackStats := scenario.StatsProxy != "" && scenario.StatsCodec != ""
	var before, after *monitor.CodecStats
	if trackStats {
		if fs, err := s.monitor.Status(); err == nil {
			cs := monitor.StatsOf(fs, scenario.StatsProxy, scenario.StatsCodec)
			before = &cs
		}
	}

	run := bench.Run(scenario.Kind, scenario.Base, scenario.Proxy, scenario.Target, direction, pattern, size, seed)

	if trackStats && before != nil {
		if fs, err := s.monitor.Status(); err == nil {
			cs := monitor.StatsOf(fs, scenario.StatsProxy, scenario.StatsCodec)
			after = &cs
		}
	}

	result := &BenchResult{
		Scenario:       scenario,
		Direction:      direction,
		Pattern:        patternStr,
		Size:           size,
		Seed:           seed,
		DurationMs:     run.DurationMs,
		ThroughputMBps: run.ThroughputMB,
		SHA256:         run.SHA256,
		OK:             run.OK,
		Error:          run.Error,
	}

	if before != nil && after != nil {
		raw := monitor.CodecStats{
			IncomingToCodecBytes: after.IncomingToCodecBytes - before.IncomingToCodecBytes,
			CodecToOutgoingBytes: after.CodecToOutgoingBytes - before.CodecToOutgoingBytes,
			OutgoingToCodecBytes: after.OutgoingToCodecBytes - before.OutgoingToCodecBytes,
			CodecToIncomingBytes: after.CodecToIncomingBytes - before.CodecToIncomingBytes,
		}
		// Upload: app bytes enter the codec (incoming_to_codec), wire
		// bytes leave toward the WAN (codec_to_outgoing).  Download is
		// the reverse; roundtrip sums both directions.
		var appBytes, wireBytes int64
		if direction == "upload" || direction == "roundtrip" {
			appBytes += raw.IncomingToCodecBytes
			wireBytes += raw.CodecToOutgoingBytes
		}
		if direction == "download" || direction == "roundtrip" {
			appBytes += raw.CodecToIncomingBytes
			wireBytes += raw.OutgoingToCodecBytes
		}
		stats := &BenchStats{
			Proxy:     scenario.StatsProxy,
			Codec:     scenario.StatsCodec,
			AppBytes:  appBytes,
			WireBytes: wireBytes,
			Raw:       raw,
		}
		if appBytes > 0 {
			ratio := float64(wireBytes) / float64(appBytes)
			stats.Ratio = &ratio
		}
		result.Stats = stats
	}
	return result
}
