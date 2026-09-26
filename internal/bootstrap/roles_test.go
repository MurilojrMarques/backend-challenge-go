package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRoles(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		raw     string
		want    []string
		wantErr string
	}{
		{name: "empty enables every role", raw: "", want: []string{"api", "consumer", "outbox", "pending"}},
		{name: "blank enables every role", raw: "   ", want: []string{"api", "consumer", "outbox", "pending"}},
		{name: "single role", raw: "api", want: []string{"api"}},
		{name: "trims and lowercases", raw: " API , Outbox ", want: []string{"api", "outbox"}},
		{name: "ignores empty parts", raw: "api,,consumer,", want: []string{"api", "consumer"}},
		{name: "duplicates collapse", raw: "api,api", want: []string{"api"}},
		{name: "unknown role", raw: "api,scheduler", wantErr: `unknown role "scheduler"`},
		{name: "only separators", raw: ",,", wantErr: "no roles given"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			roles, err := ParseRoles(tc.raw)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				assert.Nil(t, roles)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, roles.Strings())
		})
	}
}

func TestRolesMembership(t *testing.T) {
	t.Parallel()
	roles := Roles{RoleAPI: {}, RolePending: {}}

	assert.True(t, roles.Has(RoleAPI))
	assert.False(t, roles.Has(RoleOutbox))
	assert.True(t, roles.Any(RoleOutbox, RolePending))
	assert.False(t, roles.Any(RoleOutbox, RoleConsumer))
	assert.False(t, roles.Any())
}
