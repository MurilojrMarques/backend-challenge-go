package postgres

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Health struct {
	pool *pgxpool.Pool
}

func NewHealth(pool *pgxpool.Pool) *Health {
	return &Health{pool: pool}
}

func (h *Health) Name() string {
	return "postgres"
}

func (h *Health) Check(ctx context.Context) error {
	return Ping(ctx, h.pool)
}

func qualified(columns, prefix string) string {
	parts := strings.Split(columns, ",")
	for i, p := range parts {
		parts[i] = prefix + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}
