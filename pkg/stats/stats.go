package stats

import (
	"io/fs"
	"path/filepath"
	"syscall"

	"github.com/autobrr/go-qbittorrent"
	"github.com/c9s/goprocinfo/linux"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
)

type ClientStatsReader interface {
	GetActiveDownloads() int
}

type ClientTorrent struct {
	AddedOn           int64   `json:"added_on"`
	AmountLeft        int64   `json:"amount_left"`
	Category          string  `json:"category"`
	Completed         int64   `json:"completed"`
	CompletionOn      int64   `json:"completion_on"`
	DlLimit           int64   `json:"dl_limit"`
	DlSpeed           int64   `json:"dlspeed"`
	Downloaded        int64   `json:"downloaded"`
	DownloadedSession int64   `json:"downloaded_session"`
	ETA               int64   `json:"eta"`
	Hash              string  `json:"hash"`
	InfohashV1        string  `json:"infohash_v1"`
	InfohashV2        string  `json:"infohash_v2"`
	Name              string  `json:"name"`
	NumLeechs         int64   `json:"num_leechs"`
	NumSeeds          int64   `json:"num_seeds"`
	Priority          int64   `json:"priority"`
	Progress          float64 `json:"progress"`
	Ratio             float64 `json:"ratio"`
	SeedingTime       int64   `json:"seeding_time"`
	Size              int64   `json:"size"`
	State             string  `json:"state"`
	Tags              string  `json:"tags"`
	TimeActive        int64   `json:"time_active"`
	TotalSize         int64   `json:"total_size"`
	Tracker           string  `json:"tracker"`
	TrackersCount     int64   `json:"trackers_count"`
	UpLimit           int64   `json:"up_limit"`
	Uploaded          int64   `json:"uploaded"`
	UploadedSession   int64   `json:"uploaded_session"`
	UpSpeed           int64   `json:"upspeed"`
}

type ClientStatus string

const (
	ClientStatusReady    ClientStatus = "READY"
	ClientStatusNotReady ClientStatus = "NOT_READY"
)

// NotReadyReason explains why a client is not ready to accept new downloads.
// A client may report multiple reasons at once.
type NotReadyReason string

const (
	ReasonMaxDownloadsReached NotReadyReason = "max_downloads_reached"
	ReasonDiskFull            NotReadyReason = "disk_full"
	ReasonClientUnreachable   NotReadyReason = "client_unreachable"
	ReasonStorageUnavailable  NotReadyReason = "storage_unavailable"
)

type ClientStats struct {
	Name                      string                `json:"name"`
	ActiveDownloadsCount      int                   `json:"active_downloads_count"`
	ActiveDownloads           []qbittorrent.Torrent `json:"active_downloads"`
	MaxActiveDownloadsAllowed int                   `json:"max_active_downloads_allowed"`
	Status                    ClientStatus          `json:"status"`
	Reasons                   []NotReadyReason      `json:"reasons,omitempty"` // why the client is NOT_READY (may be several)
	// StorageHeadroom is how many bytes can still be added before a storage
	// rule breaks, with the remaining bytes of active downloads reserved. nil
	// when the client has no storage rules, so there is nothing to check.
	StorageHeadroom *uint64 `json:"storage_headroom,omitempty"`
}

func (c *ClientStats) HasAvailableSlot() bool {
	return c.ActiveDownloadsCount < c.MaxActiveDownloadsAllowed
}

// HasRoomFor reports whether a torrent of size bytes fits within the client's
// storage rules. An unknown size (0) or a client without storage rules always fits.
func (c *ClientStats) HasRoomFor(size uint64) bool {
	if size == 0 || c.StorageHeadroom == nil {
		return true
	}

	return *c.StorageHeadroom >= size
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
		ActiveDownloadsCount: count,
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

// GetDiskInfoByPath reads disk stats for path via statfs. Unlike GetDiskInfo it
// returns the error, so callers can tell an unreadable path apart from a full disk.
//
// Free is the space available to unprivileged users (Bavail), not Bfree as in
// linux.ReadDisk, which counts root-reserved blocks the agent cannot write to.
func GetDiskInfoByPath(path string) (*linux.Disk, error) {
	if path == "" {
		path = "/"
	}

	fs := syscall.Statfs_t{}
	if err := syscall.Statfs(path, &fs); err != nil {
		return nil, errors.Wrapf(err, "could not read disk stats for path %q", path)
	}

	bsize := uint64(fs.Bsize)

	disk := &linux.Disk{
		All:        fs.Blocks * bsize,
		Free:       fs.Bavail * bsize,
		Used:       (fs.Blocks - fs.Bfree) * bsize,
		FreeInodes: fs.Ffree,
	}

	return disk, nil
}

// GetDirUsage returns the disk space used by the files under path, the way du
// counts it: allocated blocks rather than apparent size, with hardlinked files
// counted once. Entries below path that cannot be read are skipped (e.g. a
// root-owned lost+found on a mount point); an unreadable path itself is an error.
func GetDirUsage(path string) (uint64, error) {
	var total uint64
	seen := map[[2]uint64]struct{}{}

	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == path {
				return err
			}

			log.Debug().Err(err).Msgf("skipping unreadable path %q", p)
			return nil
		}

		info, err := d.Info()
		if err != nil {
			log.Debug().Err(err).Msgf("skipping unreadable path %q", p)
			return nil
		}

		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			total += uint64(info.Size())
			return nil
		}

		if !d.IsDir() && st.Nlink > 1 {
			key := [2]uint64{uint64(st.Dev), uint64(st.Ino)}
			if _, ok := seen[key]; ok {
				return nil
			}
			seen[key] = struct{}{}
		}

		total += uint64(st.Blocks) * 512

		return nil
	})
	if err != nil {
		return 0, errors.Wrapf(err, "could not read dir usage for path %q", path)
	}

	return total, nil
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
