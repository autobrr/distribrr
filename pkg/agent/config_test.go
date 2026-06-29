package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStorageRule_allows(t *testing.T) {
	const gb = 1_000_000_000 // humanize.ParseBytes treats "GB" as SI (10^9)

	tests := []struct {
		name    string
		rule    StorageRule
		free    uint64
		used    uint64
		want    bool
		wantErr bool
	}{
		{name: "no thresholds always allowed", rule: StorageRule{Path: "/data"}, free: 0, used: 100 * gb, want: true},
		{name: "free above minFree", rule: StorageRule{MinFree: "50GB"}, free: 60 * gb, want: true},
		{name: "free below minFree", rule: StorageRule{MinFree: "50GB"}, free: 40 * gb, want: false},
		{name: "free exactly minFree", rule: StorageRule{MinFree: "50GB"}, free: 50 * gb, want: true},
		{name: "used below maxUsage", rule: StorageRule{MaxUsage: "1200GB"}, used: 1000 * gb, want: true},
		{name: "used above maxUsage", rule: StorageRule{MaxUsage: "1200GB"}, used: 1300 * gb, want: false},
		{name: "both satisfied", rule: StorageRule{MinFree: "50GB", MaxUsage: "1200GB"}, free: 60 * gb, used: 1000 * gb, want: true},
		{name: "minFree ok but maxUsage exceeded", rule: StorageRule{MinFree: "50GB", MaxUsage: "1200GB"}, free: 60 * gb, used: 1300 * gb, want: false},
		{name: "invalid minFree is ignored but errors", rule: StorageRule{MinFree: "lots"}, free: 0, want: true, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.rule.allows(tt.free, tt.used)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}
