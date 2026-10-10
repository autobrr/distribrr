package stats

import (
	"os"
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

func TestGetDirUsage(t *testing.T) {
	const size = 1 << 20

	t.Run("counts hardlinked files once", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "file")
		require.NoError(t, os.WriteFile(file, make([]byte, size), 0o644))

		single, err := GetDirUsage(dir)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, single, uint64(size))

		require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
		require.NoError(t, os.Link(file, filepath.Join(dir, "sub", "link")))

		linked, err := GetDirUsage(dir)
		require.NoError(t, err)
		// only the new directory adds blocks, not a second copy of the file
		assert.Less(t, linked, single+size)
	})

	t.Run("skips unreadable subdirectories", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can read any directory")
		}

		dir := t.TempDir()
		locked := filepath.Join(dir, "locked")
		require.NoError(t, os.Mkdir(locked, 0o000))
		t.Cleanup(func() { os.Chmod(locked, 0o755) })

		_, err := GetDirUsage(dir)
		assert.NoError(t, err)
	})

	t.Run("missing path", func(t *testing.T) {
		_, err := GetDirUsage(filepath.Join(t.TempDir(), "does-not-exist"))
		assert.Error(t, err)
	})
}
