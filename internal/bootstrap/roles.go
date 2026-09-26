package bootstrap

import (
	"fmt"
	"sort"
	"strings"
)

type Role string

const (
	RoleAPI      Role = "api"
	RoleConsumer Role = "consumer"
	RoleOutbox   Role = "outbox"
	RolePending  Role = "pending"
)

var allRoles = []Role{RoleAPI, RoleConsumer, RoleOutbox, RolePending}

type Roles map[Role]struct{}

func ParseRoles(raw string) (Roles, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return AllRoles(), nil
	}

	roles := make(Roles)
	for _, part := range strings.Split(raw, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		role := Role(name)
		if !role.valid() {
			return nil, fmt.Errorf("unknown role %q (valid: %s)", name, joinRoles(allRoles))
		}
		roles[role] = struct{}{}
	}
	if len(roles) == 0 {
		return nil, fmt.Errorf("no roles given (valid: %s)", joinRoles(allRoles))
	}
	return roles, nil
}

func AllRoles() Roles {
	roles := make(Roles, len(allRoles))
	for _, r := range allRoles {
		roles[r] = struct{}{}
	}
	return roles
}

func (r Roles) Has(role Role) bool {
	_, ok := r[role]
	return ok
}

func (r Roles) Strings() []string {
	out := make([]string, 0, len(r))
	for role := range r {
		out = append(out, string(role))
	}
	sort.Strings(out)
	return out
}

func (r Role) valid() bool {
	for _, known := range allRoles {
		if r == known {
			return true
		}
	}
	return false
}

func joinRoles(roles []Role) string {
	names := make([]string, len(roles))
	for i, r := range roles {
		names[i] = string(r)
	}
	return strings.Join(names, ", ")
}

func (r Roles) Any(roles ...Role) bool {
	for _, role := range roles {
		if r.Has(role) {
			return true
		}
	}
	return false
}
