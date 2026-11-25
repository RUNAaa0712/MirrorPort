package main

import (
	"testing"
	"time"
)

func TestPerformanceCounterAggregation(t *testing.T) {
	assert := func(name string, actual *float64, expected float64) {
		t.Helper()
		if actual == nil || *actual != expected {
			t.Errorf("%s: got %v, want %v", name, actual, expected)
		}
	}
	assert("CPU busy time", cpuUsage(cpuTimes{1000, 700}, cpuTimes{1100, 725}), 75)
	if cpuUsage(cpuTimes{}, cpuTimes{1100, 725}) != nil {
		t.Error("initial sample has no CPU usage")
	}
	assert("busiest GPU engine", busiestGPU([]counterReading{
		{"pid_1_luid_0x0_0x8_phys_0_eng_0_engtype_3D", 32.5},
		{"pid_2_luid_0x0_0x8_phys_0_eng_0_engtype_3D", 42.5},
		{"pid_2_luid_0x0_0x8_phys_0_eng_1_engtype_Copy", 40},
		{"pid_2_luid_0x0_0x9_phys_0_eng_0_engtype_3D", 3},
	}), 75)
	assert("busiest physical disk", busiestDisk([]counterReading{{"_Total", 5}, {"0 C:", 35}, {"1 D:", 82}}), 65)
	if busiestGPU(nil) != nil || busiestDisk(nil) != nil {
		t.Error("missing performance counters should not be presented as idle")
	}
}

func TestPerformanceMonitorSamples(t *testing.T) {
	monitor := startPerformanceMonitor()
	defer close(monitor.stop)
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := monitor.snapshot()
		if len(snapshot.Samples) >= 2 {
			first, next := snapshot.Samples[0], snapshot.Samples[1]
			if first.At >= next.At || next.CPU == nil || next.Memory == nil ||
				snapshot.MemoryTotal == 0 || snapshot.MemoryUsed > snapshot.MemoryTotal {
				t.Fatalf("invalid system samples: %+v", snapshot)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("the Windows performance monitor did not record two samples")
}
