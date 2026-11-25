package main

import (
	"math"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const performanceHistory = 60

type performanceSample struct {
	At     int64    `json:"at"`
	CPU    *float64 `json:"cpu"`
	Memory *float64 `json:"memory"`
	GPU    *float64 `json:"gpu"`
	Disk   *float64 `json:"disk"`
}

type performanceSnapshot struct {
	Samples     []performanceSample `json:"samples"`
	LogicalCPUs int                 `json:"logicalCPUs"`
	MemoryUsed  uint64              `json:"memoryUsed"`
	MemoryTotal uint64              `json:"memoryTotal"`
}

type performanceMonitor struct {
	mu      sync.Mutex
	samples [performanceHistory]performanceSample
	next    int
	count   int
	memory  memoryStatus
	stop    chan struct{}
}

func startPerformanceMonitor() *performanceMonitor {
	m := &performanceMonitor{stop: make(chan struct{})}
	go m.run()
	return m
}

func (m *performanceMonitor) snapshot() *performanceSnapshot {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result := &performanceSnapshot{
		Samples: make([]performanceSample, m.count), LogicalCPUs: runtime.NumCPU(),
		MemoryTotal: m.memory.totalPhysical, MemoryUsed: m.memory.totalPhysical - m.memory.availablePhysical,
	}
	start := (m.next + len(m.samples) - m.count) % len(m.samples)
	for i := range result.Samples {
		result.Samples[i] = m.samples[(start+i)%len(m.samples)]
	}
	return result
}

var (
	perfKernel         = syscall.NewLazyDLL("kernel32.dll")
	getSystemTimes     = perfKernel.NewProc("GetSystemTimes")
	globalMemoryStatus = perfKernel.NewProc("GlobalMemoryStatusEx")
	pdh                = syscall.NewLazyDLL("pdh.dll")
	pdhOpen            = pdh.NewProc("PdhOpenQueryW")
	pdhAdd             = pdh.NewProc("PdhAddEnglishCounterW")
	pdhClose           = pdh.NewProc("PdhCloseQuery")
	pdhCollect         = pdh.NewProc("PdhCollectQueryData")
	pdhCounterArray    = pdh.NewProc("PdhGetFormattedCounterArrayW")
)

type cpuTimes struct {
	total, idle uint64
}

func readCPUTimes() cpuTimes {
	var idle, kernel, user syscall.Filetime
	if ok, _, _ := getSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user))); ok == 0 {
		return cpuTimes{}
	}
	ticks := func(f syscall.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }
	return cpuTimes{total: ticks(kernel) + ticks(user), idle: ticks(idle)}
}

func cpuUsage(previous, current cpuTimes) *float64 {
	if previous.total == 0 || current.total <= previous.total || current.idle < previous.idle {
		return nil
	}
	total, idle := current.total-previous.total, current.idle-previous.idle
	if idle > total {
		return nil
	}
	return percent(100 * float64(total-idle) / float64(total))
}

type memoryStatus struct {
	length, load                                                       uint32
	totalPhysical, availablePhysical, totalPageFile, availablePageFile uint64
	totalVirtual, availableVirtual, availableExtended                  uint64
}

func readMemory() memoryStatus {
	var memory memoryStatus
	memory.length = uint32(unsafe.Sizeof(memory))
	if ok, _, _ := globalMemoryStatus.Call(uintptr(unsafe.Pointer(&memory))); ok == 0 || memory.availablePhysical > memory.totalPhysical {
		return memoryStatus{}
	}
	return memory
}

func percent(value float64) *float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	result := math.Round(min(100, max(0, value))*10) / 10
	return &result
}

func (m *performanceMonitor) run() {
	var query, gpu, disk uintptr
	if code, _, _ := pdhOpen.Call(0, 0, uintptr(unsafe.Pointer(&query))); code == 0 {
		defer pdhClose.Call(query)
		add := func(path string) uintptr {
			name, _ := syscall.UTF16PtrFromString(path)
			var handle uintptr
			if result, _, _ := pdhAdd.Call(query, uintptr(unsafe.Pointer(name)), 0, uintptr(unsafe.Pointer(&handle))); result != 0 {
				return 0
			}
			return handle
		}
		gpu = add(`\GPU Engine(*)\Utilization Percentage`)
		disk = add(`\PhysicalDisk(*)\% Idle Time`)
	}
	previous := cpuTimes{}
	next := time.NewTicker(time.Second)
	defer next.Stop()
	for {
		current := readCPUTimes()
		sample := performanceSample{At: time.Now().UnixMilli(), CPU: cpuUsage(previous, current)}
		previous = current
		memory := readMemory()
		if memory.totalPhysical != 0 {
			sample.Memory = percent(100 * float64(memory.totalPhysical-memory.availablePhysical) / float64(memory.totalPhysical))
		}
		if query != 0 {
			if result, _, _ := pdhCollect.Call(query); result == 0 {
				sample.GPU = busiestGPU(readCounterArray(gpu))
				sample.Disk = busiestDisk(readCounterArray(disk))
			}
		}
		m.mu.Lock()
		m.samples[m.next] = sample
		m.next = (m.next + 1) % len(m.samples)
		m.count = min(m.count+1, len(m.samples))
		m.memory = memory
		m.mu.Unlock()
		select {
		case <-m.stop:
			return
		case <-next.C:
		}
	}
}

type counterReading struct {
	name  string
	value float64
}

// PDH_FMT_COUNTERVALUE_ITEM_W for the x64 build. The data union is a double.
type counterItem struct {
	name   uintptr
	status uint32
	_      uint32
	value  float64
}

func readCounterArray(counter uintptr) []counterReading {
	if counter == 0 || runtime.GOARCH != "amd64" {
		return nil
	}
	const pdhFmtDouble = 0x200
	const pdhMoreData = 0x800007d2
	var size, count uint32
	code, _, _ := pdhCounterArray.Call(counter, pdhFmtDouble, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), 0)
	if uint32(code) != pdhMoreData || size == 0 || size > 2<<20 {
		return nil
	}
	words := make([]uint64, (size+7)/8)
	buffer := unsafe.Pointer(&words[0])
	code, _, _ = pdhCounterArray.Call(counter, pdhFmtDouble, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), uintptr(buffer))
	if code != 0 || uintptr(count)*unsafe.Sizeof(counterItem{}) > uintptr(size) || int(size) > len(words)*8 {
		return nil
	}
	items := unsafe.Slice((*counterItem)(buffer), int(count))
	readings := make([]counterReading, 0, len(items))
	for _, item := range items {
		if item.status > 1 || math.IsNaN(item.value) || math.IsInf(item.value, 0) {
			continue
		}
		offset := item.name - uintptr(buffer)
		if offset >= uintptr(size) || offset%2 != 0 {
			continue
		}
		name := unsafe.Slice((*uint16)(unsafe.Add(buffer, offset)), (uintptr(size)-offset)/2)
		readings = append(readings, counterReading{syscall.UTF16ToString(name), item.value})
	}
	return readings
}

func busiestGPU(readings []counterReading) *float64 {
	engines := make(map[string]float64)
	var peak float64
	for _, reading := range readings {
		_, engine, ok := strings.Cut(reading.name, "_luid_")
		if !ok {
			continue
		}
		engine, _, _ = strings.Cut(engine, "_engtype_")
		engines[engine] += max(0, reading.value)
		peak = max(peak, engines[engine])
	}
	if len(engines) == 0 {
		return nil
	}
	return percent(peak)
}

func busiestDisk(readings []counterReading) *float64 {
	var peak, total *float64
	for _, reading := range readings {
		busy := 100 - reading.value
		if reading.name == "_Total" {
			total = percent(busy)
		} else if peak == nil || busy > *peak {
			peak = percent(busy)
		}
	}
	if peak != nil {
		return peak
	}
	return total
}
