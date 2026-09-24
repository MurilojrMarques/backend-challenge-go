package main

import (
	"log/slog"
	"testing"
	"time"
)

func TestDurationEnv(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		unset   bool
		want    time.Duration
		wantErr bool
	}{
		{name: "unset uses default", unset: true, want: 30 * time.Second},
		{name: "empty uses default", value: "", want: 30 * time.Second},
		{name: "valid", value: "45s", want: 45 * time.Second},
		{name: "composite", value: "1m30s", want: 90 * time.Second},
		{name: "zero rejected", value: "0s", wantErr: true},
		{name: "negative rejected", value: "-5s", wantErr: true},
		{name: "missing unit rejected", value: "30", wantErr: true},
		{name: "garbage rejected", value: "soon", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const key = "TEST_DURATION_ENV"
			if !tt.unset {
				t.Setenv(key, tt.value)
			}

			got, err := durationEnv(key, 30*time.Second)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("durationEnv(%q) = %v, want error", tt.value, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("durationEnv(%q) unexpected error: %v", tt.value, err)
			}
			if got != tt.want {
				t.Fatalf("durationEnv(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestLogLevelEnv(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		unset   bool
		want    slog.Level
		wantErr bool
	}{
		{name: "unset uses default", unset: true, want: slog.LevelInfo},
		{name: "debug", value: "debug", want: slog.LevelDebug},
		{name: "case insensitive", value: "WARN", want: slog.LevelWarn},
		{name: "error", value: "error", want: slog.LevelError},
		{name: "offset", value: "INFO+2", want: slog.LevelInfo + 2},
		{name: "garbage rejected", value: "loud", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const key = "TEST_LOG_LEVEL_ENV"
			if !tt.unset {
				t.Setenv(key, tt.value)
			}

			got, err := logLevelEnv(key, slog.LevelInfo)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("logLevelEnv(%q) = %v, want error", tt.value, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("logLevelEnv(%q) unexpected error: %v", tt.value, err)
			}
			if got != tt.want {
				t.Fatalf("logLevelEnv(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}
