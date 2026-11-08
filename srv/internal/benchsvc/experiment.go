package benchsvc

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/hangtiancheng/wanproxy/srv/internal/monitor"
	"github.com/hangtiancheng/wanproxy/srv/internal/payload"
)

const (
	maxArms           = 8
	maxIterations     = 20
	defaultIterations = 3
	maxExperimentSize = 1 << 30
	maxExperimentSeed = int64(1<<31 - 1)
)

type ExperimentRequest struct {
	Arms []string `json:"arms"`

	Baseline *string `json:"baseline"`

	Direction  *string `json:"direction"`
	Pattern    *string `json:"pattern"`
	Size       *int64  `json:"size"`
	Seed       *int64  `json:"seed"`
	Iterations *int    `json:"iterations"`

	IsolateArms *bool `json:"isolateArms"`
}

type IterationResult struct {
	Iteration      int      `json:"iteration"`
	DurationMs     float64  `json:"durationMs"`
	ThroughputMBps float64  `json:"throughputMBps"`
	AppBytes       int64    `json:"appBytes"`
	WireBytes      int64    `json:"wireBytes"`
	Ratio          *float64 `json:"ratio"`
	SHA256         string   `json:"sha256,omitempty"`
	OK             bool     `json:"ok"`
	Error          string   `json:"error,omitempty"`
}

type ArmTotal struct {
	AppBytes           int64    `json:"appBytes"`
	WireBytes          int64    `json:"wireBytes"`
	Ratio              *float64 `json:"ratio"`
	MeanDurationMs     float64  `json:"meanDurationMs"`
	MedianDurationMs   float64  `json:"medianDurationMs"`
	TotalDurationMs    float64  `json:"totalDurationMs"`
	MeanThroughputMBps float64  `json:"meanThroughputMBps"`
	OKCount            int      `json:"okCount"`
}

type ArmReport struct {
	Scenario   monitor.Scenario  `json:"scenario"`
	HasStats   bool              `json:"hasStats"`
	Seed       int64             `json:"seed"`
	Iterations []IterationResult `json:"iterations"`
	Total      ArmTotal          `json:"total"`
}

type ComparisonVerdict struct {
	Arm               string   `json:"arm"`
	WireSavedBytes    int64    `json:"wireSavedBytes"`
	WireReductionPct  *float64 `json:"wireReductionPct"`
	ThroughputSpeedup *float64 `json:"throughputSpeedup"`
	TimeSavedMs       float64  `json:"timeSavedMs"`
	TimeSavedPct      *float64 `json:"timeSavedPct"`
}

type ExperimentReport struct {
	Pattern     string              `json:"pattern"`
	Size        int64               `json:"size"`
	Seed        int64               `json:"seed"`
	Direction   string              `json:"direction"`
	Iterations  int                 `json:"iterations"`
	Baseline    string              `json:"baseline"`
	IsolateArms bool                `json:"isolateArms"`
	Arms        []ArmReport         `json:"arms"`
	Comparison  []ComparisonVerdict `json:"comparison"`
	StartedAt   string              `json:"startedAt"`
	DurationMs  float64             `json:"durationMs"`
}

func (s *Service) RunExperiment(req ExperimentRequest) (*ExperimentReport, error) {
	if len(req.Arms) == 0 {
		return nil, errors.New("experiment requires at least one arm")
	}
	if len(req.Arms) > maxArms {
		return nil, fmt.Errorf("at most %d arms per experiment", maxArms)
	}

	direction := "download"
	if req.Direction != nil && *req.Direction != "" {
		direction = *req.Direction
	}
	if direction != "upload" && direction != "download" && direction != "roundtrip" {
		return nil, fmt.Errorf("unknown direction %q (want upload|download|roundtrip)", direction)
	}

	patternStr := "mixed"
	if req.Pattern != nil && *req.Pattern != "" {
		patternStr = *req.Pattern
	}
	pattern, err := payload.ParsePattern(patternStr)
	if err != nil {
		return nil, err
	}

	size := int64(defaultSize)
	if req.Size != nil {
		size = *req.Size
	}
	if size < 1 || size > maxExperimentSize {
		return nil, fmt.Errorf("size %d out of range 1..%d", size, maxExperimentSize)
	}

	seed := int64(42)
	if req.Seed != nil {
		seed = *req.Seed
	}
	if seed < 0 || seed > maxExperimentSeed {
		return nil, fmt.Errorf("seed %d out of range", seed)
	}

	iterations := defaultIterations
	if req.Iterations != nil {
		iterations = *req.Iterations
	}
	if iterations < 1 || iterations > maxIterations {
		return nil, fmt.Errorf("iterations %d out of range 1..%d", iterations, maxIterations)
	}

	scenarios := make([]monitor.Scenario, len(req.Arms))
	for i, id := range req.Arms {
		scenario, ok := monitor.FindScenario(id)
		if !ok {
			return nil, &ErrUnknownScenario{ID: id}
		}
		scenarios[i] = scenario
	}

	baseline := req.Arms[0]
	if req.Baseline != nil && *req.Baseline != "" {
		baseline = *req.Baseline
		found := false
		for _, id := range req.Arms {
			if id == baseline {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("baseline %q must be one of the experiment arms", baseline)
		}
	}

	if !s.begin() {
		return nil, ErrAlreadyRunning
	}
	defer s.end()

	started := time.Now()
	report := &ExperimentReport{
		Pattern:    patternStr,
		Size:       size,
		Seed:       seed,
		Direction:  direction,
		Iterations: iterations,
		Baseline:   baseline,
		Arms:       make([]ArmReport, 0, len(scenarios)),
		StartedAt:  started.UTC().Format(time.RFC3339),
	}

	isolate := true
	if req.IsolateArms != nil {
		isolate = *req.IsolateArms
	}
	report.IsolateArms = isolate

	multiplier := int64(directionMultiplier(direction))
	for i, scenario := range scenarios {
		armSeed := seed
		if isolate {
			armSeed = seed + int64(i)*7919
		}
		arm := ArmReport{
			Scenario:   scenario,
			HasStats:   scenario.StatsProxy != "" && scenario.StatsCodec != "",
			Seed:       armSeed,
			Iterations: make([]IterationResult, 0, iterations),
		}
		for iter := 1; iter <= iterations; iter++ {
			res := s.runOnceLocked(scenario, direction, patternStr, pattern, size, armSeed)
			it := IterationResult{
				Iteration:      iter,
				DurationMs:     res.DurationMs,
				ThroughputMBps: res.ThroughputMBps,
				SHA256:         res.SHA256,
				OK:             res.OK,
				Error:          res.Error,
			}
			if res.Stats != nil {
				it.AppBytes = res.Stats.AppBytes
				it.WireBytes = res.Stats.WireBytes
				it.Ratio = res.Stats.Ratio
			} else {
				app := size * multiplier
				it.AppBytes = app
				it.WireBytes = app
				one := 1.0
				it.Ratio = &one
			}
			arm.Iterations = append(arm.Iterations, it)
		}
		arm.Total = aggregateArm(arm.Iterations)
		report.Arms = append(report.Arms, arm)
	}

	report.Comparison = compareArms(report.Arms, baseline)
	report.DurationMs = float64(time.Since(started).Microseconds()) / 1000.0
	return report, nil
}

func directionMultiplier(direction string) int {
	if direction == "roundtrip" {
		return 2
	}
	return 1
}

func aggregateArm(iters []IterationResult) ArmTotal {
	var total ArmTotal
	if len(iters) == 0 {
		return total
	}
	durations := make([]float64, 0, len(iters))
	var throughputSum float64
	for _, it := range iters {
		total.AppBytes += it.AppBytes
		total.WireBytes += it.WireBytes
		total.TotalDurationMs += it.DurationMs
		throughputSum += it.ThroughputMBps
		if it.OK {
			total.OKCount++
		}
		durations = append(durations, it.DurationMs)
	}
	n := float64(len(iters))
	total.MeanDurationMs = total.TotalDurationMs / n
	total.MeanThroughputMBps = throughputSum / n
	sort.Float64s(durations)
	if len(durations)%2 == 1 {
		total.MedianDurationMs = durations[len(durations)/2]
	} else {
		mid := len(durations) / 2
		total.MedianDurationMs = (durations[mid-1] + durations[mid]) / 2
	}
	if total.AppBytes > 0 {
		ratio := float64(total.WireBytes) / float64(total.AppBytes)
		total.Ratio = &ratio
	}
	return total
}

func compareArms(arms []ArmReport, baselineID string) []ComparisonVerdict {
	var baseline *ArmReport
	for i := range arms {
		if arms[i].Scenario.ID == baselineID {
			baseline = &arms[i]
			break
		}
	}
	if baseline == nil {
		return nil
	}

	verdicts := make([]ComparisonVerdict, 0, len(arms))
	for _, arm := range arms {
		if arm.Scenario.ID == baselineID {
			continue
		}
		v := ComparisonVerdict{
			Arm:            arm.Scenario.ID,
			WireSavedBytes: baseline.Total.WireBytes - arm.Total.WireBytes,
			TimeSavedMs:    baseline.Total.TotalDurationMs - arm.Total.TotalDurationMs,
		}
		if baseline.Total.WireBytes > 0 {
			pct := 100.0 * float64(baseline.Total.WireBytes-arm.Total.WireBytes) / float64(baseline.Total.WireBytes)
			v.WireReductionPct = &pct
		}
		if arm.Total.MeanThroughputMBps > 0 && baseline.Total.MeanThroughputMBps > 0 {
			speedup := arm.Total.MeanThroughputMBps / baseline.Total.MeanThroughputMBps
			v.ThroughputSpeedup = &speedup
		}
		if baseline.Total.TotalDurationMs > 0 {
			pct := 100.0 * (baseline.Total.TotalDurationMs - arm.Total.TotalDurationMs) / baseline.Total.TotalDurationMs
			v.TimeSavedPct = &pct
		}
		verdicts = append(verdicts, v)
	}
	return verdicts
}
