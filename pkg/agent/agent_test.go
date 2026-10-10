package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/autobrr/distribrr/pkg/stats"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

// TestService_GetStatsFull_concurrent guards against the shared-state race
// (CONC-3) that previously crashed the agent when two /stats requests
// overlapped: GetStatsFull reassigned and map-wrote a shared Service.stats.
// Each call now builds its own *stats.Stats. Run with -race to exercise it.
func TestService_GetStatsFull_concurrent(t *testing.T) {
	s := &Service{clients: map[string]*QbitClient{}}

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if st := s.GetStatsFull(context.Background()); st == nil {
				t.Error("GetStatsFull returned nil")
			}
		})
	}
	wg.Wait()
}

func TestCheckStorage(t *testing.T) {
	const gb = 1_000_000_000

	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist")

	tests := []struct {
		name     string
		rules    []StorageRule
		dirUsage func(path string) (uint64, error)
		want     []stats.NotReadyReason
	}{
		{name: "no rules", rules: nil, want: nil},
		{name: "readable path within limits", rules: []StorageRule{{Path: dir, MinFree: "1B"}}, want: nil},
		{name: "readable path below minFree", rules: []StorageRule{{Path: dir, MinFree: "1000PB"}}, want: []stats.NotReadyReason{stats.ReasonDiskFull}},
		{name: "missing path is unavailable, not full", rules: []StorageRule{{Path: missing, MinFree: "1B"}}, want: []stats.NotReadyReason{stats.ReasonStorageUnavailable}},
		{
			name:  "full and unavailable reported together",
			rules: []StorageRule{{Path: dir, MinFree: "1000PB"}, {Path: missing, MinFree: "1B"}},
			want:  []stats.NotReadyReason{stats.ReasonDiskFull, stats.ReasonStorageUnavailable},
		},
		{
			name:     "dir usage below maxUsage",
			rules:    []StorageRule{{Path: dir, MaxUsage: "1200GB"}},
			dirUsage: func(string) (uint64, error) { return 1000 * gb, nil },
			want:     nil,
		},
		{
			name:     "dir usage above maxUsage",
			rules:    []StorageRule{{Path: dir, MaxUsage: "1200GB"}},
			dirUsage: func(string) (uint64, error) { return 1300 * gb, nil },
			want:     []stats.NotReadyReason{stats.ReasonDiskFull},
		},
		{
			name:     "dir usage that cannot be measured is unavailable",
			rules:    []StorageRule{{Path: dir, MaxUsage: "1200GB"}},
			dirUsage: func(string) (uint64, error) { return 0, errors.New("walk failed") },
			want:     []stats.NotReadyReason{stats.ReasonStorageUnavailable},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dirUsage := tt.dirUsage
			if dirUsage == nil {
				dirUsage = func(string) (uint64, error) {
					t.Error("dirUsage called without a maxUsage rule")
					return 0, nil
				}
			}

			st := stats.GetStats()

			got := checkStorage(zerolog.Nop(), tt.rules, st, dirUsage)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, st.DiskPathStats, missing)
		})
	}
}

func TestDirUsageCache_get(t *testing.T) {
	dir := t.TempDir()

	var c dirUsageCache

	before, err := c.get(dir)
	assert.NoError(t, err)

	assert.NoError(t, os.WriteFile(filepath.Join(dir, "file"), make([]byte, 1<<20), 0o644))

	// within the TTL the cached value is returned, not a fresh walk
	cached, err := c.get(dir)
	assert.NoError(t, err)
	assert.Equal(t, before, cached)

	_, err = c.get(filepath.Join(dir, "does-not-exist"))
	assert.Error(t, err)
}
