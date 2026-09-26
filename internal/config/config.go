package config

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	FaultConsumerAfterCommit = "consumer.after_commit_before_ack"
	FaultOutboxAfterPublish  = "outbox.after_publish_before_mark"
)

var FaultPoints = []string{FaultConsumerAfterCommit, FaultOutboxAfterPublish}

type Config struct {
	HTTP     HTTP
	Database Database
	OIDC     OIDC
	AWS      AWS
	SQS      SQS
	Outbox   Outbox
	Pending  PendingReference
	Fault    Fault
}

type HTTP struct {
	Addr            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	ShutdownTimeout time.Duration
	MaxBodyBytes    int64
}

type Database struct {
	URL            string
	ConnectTimeout time.Duration
}

type OIDC struct {
	Issuer   string
	Audience string
}

type AWS struct {
	Region      string
	EndpointURL string
}

type SQS struct {
	WagerQueueURL     string
	WagerDLQURL       string
	EventsQueueURL    string
	ConsumerName      string
	WaitTime          time.Duration
	VisibilityTimeout time.Duration
	MaxMessages       int
	RetryBackoffBase  time.Duration
	RetryBackoffMax   time.Duration
}

const maxVisibilityTimeout = 12 * time.Hour

type Outbox struct {
	PollInterval time.Duration
	BatchSize    int
	LockTTL      time.Duration
	BackoffBase  time.Duration
	BackoffMax   time.Duration
}

type PendingReference struct {
	PollInterval time.Duration
	BatchSize    int
	MaxAttempts  int
	TTL          time.Duration
	BackoffBase  time.Duration
	BackoffMax   time.Duration
}

type Fault struct {
	InjectPoint string
}

type Getenv func(string) string

func Load(getenv Getenv) (Config, error) {
	var errs []error
	env := reader{getenv: getenv, errs: &errs}

	cfg := Config{
		HTTP: HTTP{
			Addr:            env.str("HTTP_ADDR", ":8080"),
			ReadTimeout:     env.duration("HTTP_READ_TIMEOUT", 10*time.Second),
			WriteTimeout:    env.duration("HTTP_WRITE_TIMEOUT", 15*time.Second),
			ShutdownTimeout: env.duration("HTTP_SHUTDOWN_TIMEOUT", 20*time.Second),
			MaxBodyBytes:    env.int64("HTTP_MAX_BODY_BYTES", 64*1024),
		},
		Database: Database{
			URL:            env.str("DATABASE_URL", ""),
			ConnectTimeout: env.duration("DATABASE_CONNECT_TIMEOUT", 10*time.Second),
		},
		OIDC: OIDC{
			Issuer:   env.str("OIDC_ISSUER", ""),
			Audience: env.str("OIDC_AUDIENCE", ""),
		},
		AWS: AWS{
			Region:      env.str("AWS_REGION", "us-east-1"),
			EndpointURL: env.str("AWS_ENDPOINT_URL", ""),
		},
		SQS: SQS{
			WagerQueueURL:     env.str("SQS_WAGER_QUEUE_URL", ""),
			WagerDLQURL:       env.str("SQS_WAGER_DLQ_URL", ""),
			EventsQueueURL:    env.str("SQS_EVENTS_QUEUE_URL", ""),
			ConsumerName:      env.str("SQS_CONSUMER_NAME", "wager-transactions"),
			WaitTime:          env.seconds("SQS_WAIT_TIME_SECONDS", 20),
			VisibilityTimeout: env.seconds("SQS_VISIBILITY_TIMEOUT_SECONDS", 30),
			MaxMessages:       env.int("SQS_MAX_MESSAGES", 10),
			RetryBackoffBase:  env.duration("SQS_RETRY_BACKOFF_BASE", 2*time.Second),
			RetryBackoffMax:   env.duration("SQS_RETRY_BACKOFF_MAX", 4*time.Minute),
		},
		Outbox: Outbox{
			PollInterval: env.duration("OUTBOX_POLL_INTERVAL", 500*time.Millisecond),
			BatchSize:    env.int("OUTBOX_BATCH_SIZE", 50),
			LockTTL:      env.duration("OUTBOX_LOCK_TTL", 30*time.Second),
			BackoffBase:  env.duration("OUTBOX_BACKOFF_BASE", time.Second),
			BackoffMax:   env.duration("OUTBOX_BACKOFF_MAX", 5*time.Minute),
		},
		Pending: PendingReference{
			PollInterval: env.duration("PENDING_REFERENCE_POLL_INTERVAL", time.Second),
			BatchSize:    env.int("PENDING_REFERENCE_BATCH_SIZE", 50),
			MaxAttempts:  env.int("PENDING_REFERENCE_MAX_ATTEMPTS", 10),
			TTL:          env.duration("PENDING_REFERENCE_TTL", 15*time.Minute),
			BackoffBase:  env.duration("PENDING_REFERENCE_BACKOFF_BASE", 2*time.Second),
			BackoffMax:   env.duration("PENDING_REFERENCE_BACKOFF_MAX", 2*time.Minute),
		},
		Fault: Fault{
			InjectPoint: env.str("FAULT_INJECT", ""),
		},
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if c.HTTP.Addr == "" {
		fail("HTTP_ADDR is required")
	}
	if c.HTTP.MaxBodyBytes < 1024 {
		fail("HTTP_MAX_BODY_BYTES must be at least 1024")
	}
	for name, d := range map[string]time.Duration{
		"HTTP_READ_TIMEOUT":               c.HTTP.ReadTimeout,
		"HTTP_WRITE_TIMEOUT":              c.HTTP.WriteTimeout,
		"HTTP_SHUTDOWN_TIMEOUT":           c.HTTP.ShutdownTimeout,
		"DATABASE_CONNECT_TIMEOUT":        c.Database.ConnectTimeout,
		"OUTBOX_POLL_INTERVAL":            c.Outbox.PollInterval,
		"OUTBOX_LOCK_TTL":                 c.Outbox.LockTTL,
		"OUTBOX_BACKOFF_BASE":             c.Outbox.BackoffBase,
		"OUTBOX_BACKOFF_MAX":              c.Outbox.BackoffMax,
		"PENDING_REFERENCE_POLL_INTERVAL": c.Pending.PollInterval,
		"PENDING_REFERENCE_TTL":           c.Pending.TTL,
		"PENDING_REFERENCE_BACKOFF_BASE":  c.Pending.BackoffBase,
		"PENDING_REFERENCE_BACKOFF_MAX":   c.Pending.BackoffMax,
	} {
		if d <= 0 {
			fail("%s must be positive", name)
		}
	}

	if err := requireURL(c.Database.URL, "postgres", "postgresql"); err != nil {
		fail("DATABASE_URL: %v", err)
	}
	if err := requireURL(c.OIDC.Issuer, "http", "https"); err != nil {
		fail("OIDC_ISSUER: %v", err)
	}
	if c.OIDC.Audience == "" {
		fail("OIDC_AUDIENCE is required")
	}
	if c.AWS.Region == "" {
		fail("AWS_REGION is required")
	}
	if c.AWS.EndpointURL != "" {
		if err := requireURL(c.AWS.EndpointURL, "http", "https"); err != nil {
			fail("AWS_ENDPOINT_URL: %v", err)
		}
	}
	for name, u := range map[string]string{
		"SQS_WAGER_QUEUE_URL":  c.SQS.WagerQueueURL,
		"SQS_WAGER_DLQ_URL":    c.SQS.WagerDLQURL,
		"SQS_EVENTS_QUEUE_URL": c.SQS.EventsQueueURL,
	} {
		if err := requireURL(u, "http", "https"); err != nil {
			fail("%s: %v", name, err)
		}
	}
	if c.SQS.ConsumerName == "" {
		fail("SQS_CONSUMER_NAME is required")
	}
	if c.SQS.WaitTime < 0 || c.SQS.WaitTime > 20*time.Second {
		fail("SQS_WAIT_TIME_SECONDS must be between 0 and 20")
	}
	if c.SQS.VisibilityTimeout < time.Second || c.SQS.VisibilityTimeout > maxVisibilityTimeout {
		fail("SQS_VISIBILITY_TIMEOUT_SECONDS must be between 1 and 43200")
	}
	if c.SQS.MaxMessages < 1 || c.SQS.MaxMessages > 10 {
		fail("SQS_MAX_MESSAGES must be between 1 and 10")
	}
	if c.SQS.RetryBackoffBase <= 0 || c.SQS.RetryBackoffMax < c.SQS.RetryBackoffBase || c.SQS.RetryBackoffMax > maxVisibilityTimeout {
		fail("SQS_RETRY_BACKOFF_BASE must be positive and SQS_RETRY_BACKOFF_MAX between it and 12h")
	}
	if c.Fault.InjectPoint != "" && !slices.Contains(FaultPoints, c.Fault.InjectPoint) {
		fail("FAULT_INJECT must be one of %s", strings.Join(FaultPoints, ", "))
	}
	if c.Outbox.BatchSize < 1 {
		fail("OUTBOX_BATCH_SIZE must be at least 1")
	}
	if c.Outbox.BackoffMax < c.Outbox.BackoffBase {
		fail("OUTBOX_BACKOFF_MAX must be at least OUTBOX_BACKOFF_BASE")
	}
	if c.Pending.BatchSize < 1 {
		fail("PENDING_REFERENCE_BATCH_SIZE must be at least 1")
	}
	if c.Pending.MaxAttempts < 1 {
		fail("PENDING_REFERENCE_MAX_ATTEMPTS must be at least 1")
	}
	if c.Pending.BackoffMax < c.Pending.BackoffBase {
		fail("PENDING_REFERENCE_BACKOFF_MAX must be at least PENDING_REFERENCE_BACKOFF_BASE")
	}

	return errors.Join(errs...)
}

func requireURL(raw string, schemes ...string) error {
	if raw == "" {
		return errors.New("is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	for _, s := range schemes {
		if u.Scheme == s && u.Host != "" {
			return nil
		}
	}
	return fmt.Errorf("must use scheme %s and include a host", strings.Join(schemes, " or "))
}

type reader struct {
	getenv Getenv
	errs   *[]error
}

func (r reader) raw(name string) (string, bool) {
	v := strings.TrimSpace(r.getenv(name))
	return v, v != ""
}

func (r reader) fail(name string, err error) {
	*r.errs = append(*r.errs, fmt.Errorf("%s: %w", name, err))
}

func (r reader) str(name, def string) string {
	if v, ok := r.raw(name); ok {
		return v
	}
	return def
}

func (r reader) int(name string, def int) int {
	v, ok := r.raw(name)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		r.fail(name, err)
		return def
	}
	return n
}

func (r reader) int64(name string, def int64) int64 {
	v, ok := r.raw(name)
	if !ok {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		r.fail(name, err)
		return def
	}
	return n
}

func (r reader) duration(name string, def time.Duration) time.Duration {
	v, ok := r.raw(name)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		r.fail(name, err)
		return def
	}
	return d
}

func (r reader) seconds(name string, def int) time.Duration {
	return time.Duration(r.int(name, def)) * time.Second
}
