package diskusage

import (
	"syscall"

	"github.com/dustin/go-humanize"
)

// DiskUsage contains usage data and provides user-friendly access methods
type DiskUsage struct {
	stat *syscall.Statfs_t
}

// NewDiskUsage returns an object holding the disk usage of volumePath
// or nil in case of error (invalid path, etc)
func NewDiskUsage(volumePath string) *DiskUsage {
	var stat syscall.Statfs_t
	syscall.Statfs(volumePath, &stat)
	return &DiskUsage{&stat}
}

// Free returns total free bytes on file system
func (du *DiskUsage) Free() uint64 {
	return du.stat.Bfree * uint64(du.stat.Bsize)
}

// FreeString returns human-readable string
func (du *DiskUsage) FreeString() string {
	return humanize.Bytes(du.Free())
}

// Available return total available bytes on file system to an unprivileged user
func (du *DiskUsage) Available() uint64 {
	return du.stat.Bavail * uint64(du.stat.Bsize)
}

// AvailableString returns human-readable string
func (du *DiskUsage) AvailableString() string {
	return humanize.Bytes(du.Available())
}

// Size returns total size in bytes of the file system
func (du *DiskUsage) Size() uint64 {
	return du.stat.Blocks * uint64(du.stat.Bsize)
}

// SizeString returns human-readable string
func (du *DiskUsage) SizeString() string {
	return humanize.Bytes(du.Size())
}

// Used returns total bytes used in file system
func (du *DiskUsage) Used() uint64 {
	return du.Size() - du.Free()
}

// UsedString returns human-readable string
func (du *DiskUsage) UsedString() string {
	return humanize.Bytes(du.Used())
}

// Usage returns percentage of use on the file system
func (du *DiskUsage) Usage() float32 {
	return float32(du.Used()) / float32(du.Size())
}

//// UsageString returns human-readable string
//func (du *DiskUsage) UsageString() string {
//	return humanize.FormatFloat(du.Usage())
//}
