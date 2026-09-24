package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTargetURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		addr    string
		path    string
		want    string
		wantErr bool
	}{
		{name: "empty addr uses default", addr: "", path: "/health/ready", want: "http://127.0.0.1:8080/health/ready"},
		{name: "port only", addr: ":9090", path: "/health/ready", want: "http://127.0.0.1:9090/health/ready"},
		{name: "ipv4 wildcard", addr: "0.0.0.0:8080", path: "/health/ready", want: "http://127.0.0.1:8080/health/ready"},
		{name: "ipv6 wildcard", addr: "[::]:8080", path: "/health/ready", want: "http://127.0.0.1:8080/health/ready"},
		{name: "specific host kept", addr: "10.0.0.5:8080", path: "/health/ready", want: "http://10.0.0.5:8080/health/ready"},
		{name: "ipv6 host joined", addr: "[::1]:8080", path: "/health/live", want: "http://[::1]:8080/health/live"},
		{name: "path without slash", addr: ":8080", path: "health/live", want: "http://127.0.0.1:8080/health/live"},
		{name: "missing port", addr: "8080", path: "/health/ready", wantErr: true},
		{name: "garbage", addr: "not-an-addr", path: "/health/ready", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := targetURL(tt.addr, tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("targetURL(%q) = %q, want error", tt.addr, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("targetURL(%q) unexpected error: %v", tt.addr, err)
			}
			if got != tt.want {
				t.Fatalf("targetURL(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

func TestCheck(t *testing.T) {
	t.Parallel()

	t.Run("2xx is healthy", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()

		if err := check(context.Background(), newClient(), srv.URL); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("non-2xx reports status and body", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"down","postgres":"unreachable"}`))
		}))
		defer srv.Close()

		err := check(context.Background(), newClient(), srv.URL)
		if err == nil {
			t.Fatal("expected error")
		}
		for _, want := range []string{"503", "postgres"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("body is truncated", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(strings.Repeat("x", 10*maxBodyBytes)))
		}))
		defer srv.Close()

		err := check(context.Background(), newClient(), srv.URL)
		if err == nil {
			t.Fatal("expected error")
		}
		if len(err.Error()) > maxBodyBytes+len(srv.URL)+64 {
			t.Fatalf("error message too long: %d bytes", len(err.Error()))
		}
	})

	t.Run("respects context deadline", func(t *testing.T) {
		t.Parallel()
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			<-release
			w.WriteHeader(http.StatusOK)
		}))
		defer func() {
			close(release)
			srv.Close()
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		if err := check(ctx, newClient(), srv.URL); err == nil {
			t.Fatal("expected timeout error")
		}
	})

	t.Run("connection refused", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()

		if err := check(context.Background(), newClient(), url); err == nil {
			t.Fatal("expected connection error")
		}
	})
}

func TestRun(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health/ready":
			w.WriteHeader(http.StatusOK)
		case "/health/live":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	addr := strings.TrimPrefix(srv.URL, "http://")
	getenv := func(key string) string {
		if key == "HTTP_ADDR" {
			return addr
		}
		return ""
	}

	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "default path", args: nil, want: 0},
		{name: "explicit path", args: []string{"/health/live"}, want: 0},
		{name: "unknown path", args: []string{"/nope"}, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := run(tt.args, getenv); got != tt.want {
				t.Fatalf("run(%v) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}

	t.Run("invalid addr", func(t *testing.T) {
		t.Parallel()
		if got := run(nil, func(string) string { return "bad" }); got != 1 {
			t.Fatalf("run with invalid addr = %d, want 1", got)
		}
	})
}
