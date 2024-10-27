package stats

import (
	"github.com/c9s/goprocinfo/linux"
	"github.com/rs/zerolog/log"
)

type ClientStatsReader interface {
	GetActiveDownloads() int
}

type ClientStats struct {
	ActiveDownloads int  `json:"active_downloads"`
	Ready           bool `json:"ready"` // Ready is true if ActiveDownloads is less than configured
}

type Stats struct {
	MemStats      *linux.MemInfo         `json:"mem_stats"`
	DiskStats     *linux.Disk            `json:"disk_stats"`
	DiskPathStats map[string]*linux.Disk `json:"disk_path_stats"`
	CpuStats      *linux.CPUStat         `json:"cpu_stats"`
	LoadStats     *linux.LoadAvg         `json:"load_stats"`
	TaskCount     int                    `json:"task_count"`
	ClientStats   map[string]ClientStats `json:"client_stats"`
	// NetworkStats
}

func (s *Stats) MemUsedKb() uint64 {
	return s.MemStats.MemTotal - s.MemStats.MemAvailable
}

func (s *Stats) MemUsedPercent() uint64 {
	return s.MemStats.MemAvailable / s.MemStats.MemTotal
}

func (s *Stats) MemAvailableKb() uint64 {
	return s.MemStats.MemAvailable
}

func (s *Stats) MemTotalKb() uint64 {
	return s.MemStats.MemTotal
}

func (s *Stats) DiskTotal() uint64 {
	return s.DiskStats.All
}

func (s *Stats) DiskFree() uint64 {
	return s.DiskStats.Free
}

func (s *Stats) DiskUsed() uint64 {
	return s.DiskStats.Used
}

func (s *Stats) CpuUsage() float64 {

	idle := s.CpuStats.Idle + s.CpuStats.IOWait
	nonIdle := s.CpuStats.User + s.CpuStats.Nice + s.CpuStats.System + s.CpuStats.IRQ + s.CpuStats.SoftIRQ + s.CpuStats.Steal
	total := idle + nonIdle

	if total == 0 && idle == 0 {
		return 0.00
	}

	return (float64(total) - float64(idle)) / float64(total)
}

func (s *Stats) SetClientActiveDownloads(client string, count int) uint64 {
	s.ClientStats[client] = ClientStats{
		ActiveDownloads: count,
		Ready:           false,
	}
	return uint64(count)
}

func GetStats() *Stats {
	return &Stats{
		MemStats:      GetMemoryInfo(),
		DiskStats:     GetDiskInfo(),
		DiskPathStats: map[string]*linux.Disk{},
		CpuStats:      GetCpuStats(),
		LoadStats:     GetLoadAvg(),
		ClientStats:   map[string]ClientStats{},
	}
}

// GetMemoryInfo See https://godoc.org/github.com/c9s/goprocinfo/linux#MemInfo
func GetMemoryInfo() *linux.MemInfo {
	memstats, err := linux.ReadMemInfo("/proc/meminfo")
	if err != nil {
		log.Error().Err(err).Msgf("Error reading from /proc/meminfo")
		return &linux.MemInfo{}
	}

	return memstats
}

// GetDiskInfo See https://godoc.org/github.com/c9s/goprocinfo/linux#Disk
func GetDiskInfo() *linux.Disk {
	diskstats, err := linux.ReadDisk("/")
	if err != nil {
		//log.Printf("Error reading from /")
		log.Error().Err(err).Msgf("Error reading from /")
		return &linux.Disk{}
	}

	return diskstats
}

// GetDiskInfoByPath See https://godoc.org/github.com/c9s/goprocinfo/linux#Disk
func GetDiskInfoByPath(path string) *linux.Disk {
	if path == "" {
		path = "/"
	}

	diskstats, err := linux.ReadDisk(path)
	if err != nil {
		//log.("Error reading from: %q", path)
		log.Error().Err(err).Msgf("Error reading from: %q", path)
		return &linux.Disk{}
	}

	return diskstats
}

// GetCpuStats GetCpuInfo See https://godoc.org/github.com/c9s/goprocinfo/linux#CPUStat
func GetCpuStats() *linux.CPUStat {
	stats, err := linux.ReadStat("/proc/stat")
	if err != nil {
		log.Printf("Error reading from /proc/stat")
		log.Error().Err(err).Msgf("Error reading from: /proc/stat")
		return &linux.CPUStat{}
	}

	return &stats.CPUStatAll
}

// GetLoadAvg See https://godoc.org/github.com/c9s/goprocinfo/linux#LoadAvg
func GetLoadAvg() *linux.LoadAvg {
	loadavg, err := linux.ReadLoadAvg("/proc/loadavg")
	if err != nil {
		//log.Printf("Error reading from /proc/loadavg")
		log.Error().Err(err).Msgf("Error reading from: /proc/loadavg")
		return &linux.LoadAvg{}
	}

	return loadavg
}
