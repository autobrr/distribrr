package stats

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetDiskInfoByPath(t *testing.T) {
	t.Run("readable path", func(t *testing.T) {
		disk, err := GetDiskInfoByPath(t.TempDir())
		require.NoError(t, err)

		assert.NotZero(t, disk.All)
		// Free excludes root-reserved blocks, so free + used never exceeds total
		assert.LessOrEqual(t, disk.Free+disk.Used, disk.All)
	})

	t.Run("missing path", func(t *testing.T) {
		disk, err := GetDiskInfoByPath(filepath.Join(t.TempDir(), "does-not-exist"))
		assert.Error(t, err)
		assert.Nil(t, disk)
	})
}
