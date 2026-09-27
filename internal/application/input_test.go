package application_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/MurilojrMarques/backend-challenge-go/internal/application"
)

func TestCorrelationIDAcceptsOnlyASafeCharset(t *testing.T) {
	t.Parallel()
	fallback := uuid.Must(uuid.NewV7())

	for _, ok := range []string{"corr-1", "msg-123", "provider-a:bet.1_2", strings.Repeat("a", 128)} {
		assert.True(t, application.ValidCorrelationID(ok), ok)
		assert.Equal(t, ok, application.CorrelationID(ok, fallback))
	}
	for _, bad := range []string{"", "has space", "tab\t", "nul\x00", "émoji🙂", "zero\u200bwidth", "<script>", strings.Repeat("a", 129)} {
		assert.False(t, application.ValidCorrelationID(bad), bad)
		assert.Equal(t, fallback.String(), application.CorrelationID(bad, fallback), "an unsafe id is replaced by the fallback")
	}
}

func TestCleanText(t *testing.T) {
	t.Parallel()
	assert.True(t, application.CleanText("msg-123"))
	assert.True(t, application.CleanText("olá"))
	for _, bad := range []string{" padded", "padded ", "nul\x00", "line\nbreak", "\xff"} {
		assert.False(t, application.CleanText(bad), bad)
	}
}

func TestInboxRecordRejectsControlCharacters(t *testing.T) {
	t.Parallel()
	rec := application.InboxRecord{ConsumerName: "c", MessageID: "msg\x00", PayloadHash: "h", ReceivedAt: time.Now()}
	assert.ErrorIs(t, rec.Validate(), application.ErrInvalidInput)
	rec.MessageID = "msg-1"
	assert.NoError(t, rec.Validate())
}
