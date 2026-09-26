package bootstrap

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/MurilojrMarques/backend-challenge-go/internal/adapters/httpapi"
	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
	"github.com/MurilojrMarques/backend-challenge-go/internal/config"
	"github.com/MurilojrMarques/backend-challenge-go/internal/worker"
)

func TestGraphIsCompleteForEveryRoleSet(t *testing.T) {
	t.Parallel()
	for mask := 1; mask < 1<<len(allRoles); mask++ {
		roles := make(Roles)
		for i, r := range allRoles {
			if mask&(1<<i) != 0 {
				roles[r] = struct{}{}
			}
		}
		t.Run(strings.Join(roles.Strings(), "+"), func(t *testing.T) {
			t.Parallel()
			require.NoError(t, fx.ValidateApp(New(roles), fx.NopLogger))
		})
	}
}

func TestWorkersAreWiredOnlyForTheirRoles(t *testing.T) {
	t.Parallel()
	cases := []struct {
		role   Role
		target any
	}{
		{RoleConsumer, func(*worker.Consumer) {}},
		{RoleOutbox, func(*worker.OutboxRelay) {}},
		{RolePending, func(*worker.PendingResolver) {}},
	}
	for _, tc := range cases {
		t.Run(string(tc.role), func(t *testing.T) {
			t.Parallel()
			assert.NoError(t, fx.ValidateApp(New(Roles{tc.role: {}}), fx.NopLogger, fx.Invoke(tc.target)))
			assert.Error(t, fx.ValidateApp(New(Roles{RoleAPI: {}}), fx.NopLogger, fx.Invoke(tc.target)))
		})
	}
}

func TestAPIOnlyInstanceDoesNotDependOnSQS(t *testing.T) {
	t.Parallel()
	needsQueue := fx.Invoke(func(application.Queue) {})
	assert.Error(t, fx.ValidateApp(New(Roles{RoleAPI: {}}), fx.NopLogger, needsQueue))
	assert.Error(t, fx.ValidateApp(New(Roles{RolePending: {}}), fx.NopLogger, needsQueue))
	assert.NoError(t, fx.ValidateApp(New(Roles{RoleConsumer: {}}), fx.NopLogger, needsQueue))
}

func TestHTTPOptionsFollowRoles(t *testing.T) {
	t.Parallel()
	cfg := config.Config{HTTP: config.HTTP{MaxBodyBytes: 4096}}
	assert.Equal(t, httpapi.Options{EnableAPI: true, MaxBodyBytes: 4096}, httpOptions(Roles{RoleAPI: {}, RoleOutbox: {}}, cfg))
	assert.Equal(t, httpapi.Options{EnableAPI: false, MaxBodyBytes: 4096}, httpOptions(Roles{RoleOutbox: {}}, cfg))
}
