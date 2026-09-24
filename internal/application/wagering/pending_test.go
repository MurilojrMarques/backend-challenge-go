package wagering_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application/wagering"
)

func TestBackoff(t *testing.T) {
	t.Parallel()

	b := wagering.Backoff{Base: time.Second, Max: 10 * time.Second}
	assert.Equal(t, time.Second, b.Next(0))
	assert.Equal(t, time.Second, b.Next(1))
	assert.Equal(t, 2*time.Second, b.Next(2))
	assert.Equal(t, 4*time.Second, b.Next(3))
	assert.Equal(t, 8*time.Second, b.Next(4))
	assert.Equal(t, 10*time.Second, b.Next(5))
	assert.Equal(t, 10*time.Second, b.Next(100), "never overflows")

	assert.Equal(t, time.Minute, wagering.Backoff{Max: time.Minute}.Next(3), "zero base falls back to max")
}
