package benchsvc

import (
	"math"
	"testing"

	"github.com/hangtiancheng/wanproxy/srv/internal/monitor"
)

func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func TestAggregateArm(t *testing.T) {
	r := func(v float64) *float64 { return &v }
	iters := []IterationResult{
		{Iteration: 1, DurationMs: 300, ThroughputMBps: 100, AppBytes: 1000, WireBytes: 1000, Ratio: r(1.0), OK: true},
		{Iteration: 2, DurationMs: 100, ThroughputMBps: 300, AppBytes: 1000, WireBytes: 100, Ratio: r(0.1), OK: true},
		{Iteration: 3, DurationMs: 200, ThroughputMBps: 200, AppBytes: 1000, WireBytes: 100, Ratio: r(0.1), OK: false},
	}
	total := aggregateArm(iters)

	if total.AppBytes != 3000 || total.WireBytes != 1200 {
		t.Fatalf("bytes = %d/%d, want 3000/1200", total.AppBytes, total.WireBytes)
	}
	if total.Ratio == nil {
		t.Fatal("ratio is nil")
	}
	approx(t, "ratio", *total.Ratio, 0.4)
	approx(t, "mean duration", total.MeanDurationMs, 200)
	approx(t, "median duration", total.MedianDurationMs, 200)
	approx(t, "total duration", total.TotalDurationMs, 600)
	approx(t, "mean throughput", total.MeanThroughputMBps, 200)
	if total.OKCount != 2 {
		t.Fatalf("okCount = %d, want 2", total.OKCount)
	}
}

func TestAggregateArmMedianEven(t *testing.T) {
	iters := []IterationResult{
		{DurationMs: 100}, {DurationMs: 300}, {DurationMs: 200}, {DurationMs: 400},
	}
	total := aggregateArm(iters)
	approx(t, "median", total.MedianDurationMs, 250)
}

func TestAggregateArmEmpty(t *testing.T) {
	total := aggregateArm(nil)
	if total.AppBytes != 0 || total.Ratio != nil {
		t.Fatalf("empty aggregate = %+v", total)
	}
}

func TestCompareArms(t *testing.T) {
	arms := []ArmReport{
		{
			Scenario: monitor.Scenario{ID: "direct"},
			Total: ArmTotal{
				WireBytes: 1000, AppBytes: 1000,
				TotalDurationMs: 500, MeanThroughputMBps: 100,
			},
		},
		{
			Scenario: monitor.Scenario{ID: "wan-chain"},
			Total: ArmTotal{
				WireBytes: 250, AppBytes: 1000,
				TotalDurationMs: 400, MeanThroughputMBps: 250,
			},
		},
	}
	verdicts := compareArms(arms, "direct")
	if len(verdicts) != 1 {
		t.Fatalf("verdicts = %d, want 1", len(verdicts))
	}
	v := verdicts[0]
	if v.Arm != "wan-chain" {
		t.Fatalf("arm = %q", v.Arm)
	}
	if v.WireSavedBytes != 750 {
		t.Fatalf("wireSaved = %d, want 750", v.WireSavedBytes)
	}
	if v.WireReductionPct == nil || math.Abs(*v.WireReductionPct-75.0) > 1e-9 {
		t.Fatalf("wireReductionPct = %v, want 75", v.WireReductionPct)
	}
	if v.ThroughputSpeedup == nil || math.Abs(*v.ThroughputSpeedup-2.5) > 1e-9 {
		t.Fatalf("speedup = %v, want 2.5", v.ThroughputSpeedup)
	}
	approx(t, "time saved", v.TimeSavedMs, 100)
	if v.TimeSavedPct == nil || math.Abs(*v.TimeSavedPct-20.0) > 1e-9 {
		t.Fatalf("timeSavedPct = %v, want 20", v.TimeSavedPct)
	}
}

func TestCompareArmsBaselineMissing(t *testing.T) {
	arms := []ArmReport{{Scenario: monitor.Scenario{ID: "direct"}}}
	if verdicts := compareArms(arms, "nope"); verdicts != nil {
		t.Fatalf("verdicts = %+v, want nil", verdicts)
	}
}

func TestDirectionMultiplier(t *testing.T) {
	if directionMultiplier("roundtrip") != 2 {
		t.Fatal("roundtrip must count two transfers")
	}
	if directionMultiplier("upload") != 1 || directionMultiplier("download") != 1 {
		t.Fatal("upload/download must count one transfer")
	}
}
