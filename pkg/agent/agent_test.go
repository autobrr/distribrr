package agent

import (
	"context"
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
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist")

	tests := []struct {
		name  string
		rules []StorageRule
		want  []stats.NotReadyReason
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := stats.GetStats()

			got := checkStorage(zerolog.Nop(), tt.rules, st)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, st.DiskPathStats, missing)
		})
	}
}
