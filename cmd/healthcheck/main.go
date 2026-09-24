package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultAddr    = ":8080"
	defaultPath    = "/health/ready"
	requestTimeout = 2 * time.Second
	maxBodyBytes   = 512
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv))
}

func run(args []string, getenv func(string) string) int {
	path := defaultPath
	if len(args) > 0 && args[0] != "" {
		path = args[0]
	}

	url, err := targetURL(getenv("HTTP_ADDR"), path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	if err := check(ctx, newClient(), url); err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	return 0
}

func targetURL(addr, path string) (string, error) {
	if addr == "" {
		addr = defaultAddr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("invalid HTTP_ADDR %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return "http://" + net.JoinHostPort(host, port) + path, nil
}

func newClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
		},
	}
}

func check(ctx context.Context, client *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
