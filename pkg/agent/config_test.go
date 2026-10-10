package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func TestConfig_LoadFromFile_removedKeyDoesNotSurvive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	writeFile(t, path, `
agent:
  labels:
    network: 1G
    disktype: ssd
`)
	first := NewConfig()
	require.NoError(t, first.LoadFromFile(path))
	assert.Equal(t, map[string]string{"network": "1G", "disktype": "ssd"}, first.Agent.Labels)

	writeFile(t, path, `
agent:
  labels:
    network: 1G
`)
	second := NewConfig()
	require.NoError(t, second.LoadFromFile(path))
	assert.Equal(t, map[string]string{"network": "1G"}, second.Agent.Labels)
	assert.Equal(t, path, second.path)
}

func TestConfig_Validate(t *testing.T) {
	cfg := NewConfig()
	assert.Error(t, cfg.Validate(), "missing http token")

	cfg.Http.Token = "secret"
	assert.NoError(t, cfg.Validate())

	cfg.Clients["qbit"] = &QbitClient{}
	assert.Error(t, cfg.Validate(), "client without host")

	cfg.Clients["qbit"] = nil
	assert.Error(t, cfg.Validate(), "empty client entry")
}

func TestStorageRule_headroom(t *testing.T) {
	const gb = 1_000_000_000 // humanize.ParseBytes treats "GB" as SI (10^9)

	tests := []struct {
		name     string
		rule     StorageRule
		free     uint64
		used     uint64
		want     bool
		wantRoom uint64
		wantErr  bool
	}{
		{name: "no thresholds always allowed", rule: StorageRule{Path: "/data"}, free: 0, used: 100 * gb, want: true, wantRoom: 0},
		{name: "no thresholds room is free space", rule: StorageRule{Path: "/data"}, free: 70 * gb, want: true, wantRoom: 70 * gb},
		{name: "free above minFree", rule: StorageRule{MinFree: "50GB"}, free: 60 * gb, want: true, wantRoom: 10 * gb},
		{name: "free below minFree", rule: StorageRule{MinFree: "50GB"}, free: 40 * gb, want: false},
		{name: "free exactly minFree", rule: StorageRule{MinFree: "50GB"}, free: 50 * gb, want: true, wantRoom: 0},
		{name: "used below maxUsage", rule: StorageRule{MaxUsage: "1200GB"}, free: 500 * gb, used: 1000 * gb, want: true, wantRoom: 200 * gb},
		{name: "maxUsage room capped by free space", rule: StorageRule{MaxUsage: "1200GB"}, free: 100 * gb, used: 1000 * gb, want: true, wantRoom: 100 * gb},
		{name: "used above maxUsage", rule: StorageRule{MaxUsage: "1200GB"}, used: 1300 * gb, want: false},
		{name: "both satisfied takes the tighter room", rule: StorageRule{MinFree: "50GB", MaxUsage: "1200GB"}, free: 300 * gb, used: 1000 * gb, want: true, wantRoom: 200 * gb},
		{name: "minFree ok but maxUsage exceeded", rule: StorageRule{MinFree: "50GB", MaxUsage: "1200GB"}, free: 60 * gb, used: 1300 * gb, want: false},
		{name: "invalid minFree is ignored but errors", rule: StorageRule{MinFree: "lots"}, free: 0, want: true, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			room, got, err := tt.rule.headroom(tt.free, tt.used)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantRoom, room)
		})
	}
}
