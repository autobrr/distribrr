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
	ReasonStorageMeasuring    NotReadyReason = "storage_measuring" // maxUsage path not measured yet after agent start
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
	DiskStats     *linux.Disk            `json:"disk_stats"` // root filesystem, informational; nil if unreadable
	DiskPathStats map[string]*linux.Disk `json:"disk_path_stats"`
	CpuStats      *linux.CPUStat         `json:"cpu_stats"`
	LoadStats     *linux.LoadAvg         `json:"load_stats"`
	ClientStats   map[string]ClientStats `json:"client_stats"`
	// NetworkStats
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

// GetDiskInfo returns disk stats for the root filesystem, or nil if they cannot
// be read. It is informational only: scheduling uses the per-path storage rules,
// and inside a container "/" is the container's own filesystem.
func GetDiskInfo() *linux.Disk {
	disk, err := GetDiskInfoByPath("/")
	if err != nil {
		log.Error().Err(err).Msg("could not read disk stats for /")
		return nil
	}

	return disk
}

// GetDiskInfoByPath reads disk stats for path via statfs. It returns the error,
// so callers can tell an unreadable path apart from a full disk.
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
