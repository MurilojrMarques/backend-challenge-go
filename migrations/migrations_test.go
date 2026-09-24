package migrations_test

import (
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/MurilojrMarques/backend-challenge-go/migrations"
)

var namePattern = regexp.MustCompile(`^(\d{6})_([a-z0-9_]+)\.(up|down)\.sql$`)

func TestEveryMigrationHasUpAndDown(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(migrations.FS, ".")
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	type pair struct{ up, down bool }
	byVersion := map[int]*pair{}
	names := map[int]string{}

	for _, entry := range entries {
		m := namePattern.FindStringSubmatch(entry.Name())
		require.NotNil(t, m, "unexpected file name %q", entry.Name())

		version, err := strconv.Atoi(m[1])
		require.NoError(t, err)
		if existing, ok := names[version]; ok {
			assert.Equal(t, existing, m[2], "version %d used by two different names", version)
		}
		names[version] = m[2]

		p := byVersion[version]
		if p == nil {
			p = &pair{}
			byVersion[version] = p
		}
		if m[3] == "up" {
			p.up = true
		} else {
			p.down = true
		}

		content, err := fs.ReadFile(migrations.FS, entry.Name())
		require.NoError(t, err)
		assert.NotEmpty(t, content, "%s is empty", entry.Name())
	}

	versions := make([]int, 0, len(byVersion))
	for v := range byVersion {
		versions = append(versions, v)
	}
	sort.Ints(versions)

	for i, v := range versions {
		assert.Equal(t, i+1, v, "versions must be contiguous starting at 1")
		assert.True(t, byVersion[v].up, "version %d has no up migration", v)
		assert.True(t, byVersion[v].down, "version %d has no down migration", v)
	}
}
