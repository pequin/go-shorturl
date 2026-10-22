package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfig(t *testing.T) {
	cfg, err := loadConfig(envFrom(nil))
	if err != nil {
		t.Fatalf("loadConfig(empty env): %v", err)
	}
	if cfg.addr != ":8080" || cfg.baseURL.String() != "http://localhost:8080" || cfg.requestTimeout != 5*time.Second || cfg.shutdownTimeout != 15*time.Second {
		t.Errorf("defaults = %+v", cfg)
	}

	cfg, err = loadConfig(envFrom(map[string]string{"ADDR": ":9000", "BASE_URL": "https://sho.rt", "REQUEST_TIMEOUT": "2s"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != ":9000" || cfg.baseURL.Host != "sho.rt" || cfg.requestTimeout != 2*time.Second {
		t.Errorf("overrides = %+v", cfg)
	}
}

func TestLoadConfigRejects(t *testing.T) {
	tests := []struct {
		env     map[string]string
		wantErr string
	}{
		{map[string]string{"BASE_URL": "sho.rt"}, "BASE_URL"},
		{map[string]string{"BASE_URL": "ftp://sho.rt"}, "BASE_URL"},
		{map[string]string{"BASE_URL": "http://%zz"}, "BASE_URL"},
		{map[string]string{"REQUEST_TIMEOUT": "soon"}, "REQUEST_TIMEOUT"},
		{map[string]string{"SHUTDOWN_TIMEOUT": "-1s"}, "must be positive"},
		{map[string]string{"SHUTDOWN_TIMEOUT": "0s"}, "must be positive"},
	}
	for _, tt := range tests {
		_, err := loadConfig(envFrom(tt.env))
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("loadConfig(%v) error = %v, want it to mention %q", tt.env, err, tt.wantErr)
		}
	}
}

// TestRunServesAndShutsDown starts the real server on a free port, makes a
// request, then cancels the context the way SIGTERM would.
func TestRunServesAndShutsDown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	logR, logW := io.Pipe()
	addrCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(logR)
		for sc.Scan() {
			var entry struct {
				Msg  string `json:"msg"`
				Addr string `json:"addr"`
			}
			if json.Unmarshal(sc.Bytes(), &entry) == nil && entry.Msg == "listening" {
				addrCh <- entry.Addr
			}
		}
	}()

	env := envFrom(map[string]string{"ADDR": "127.0.0.1:0"})
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, env, logW)
		logW.Close()
	}()

	var addr string
	select {
	case addr = <-addrCh:
	case err := <-done:
		t.Fatalf("run exited before listening: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not report its address")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("GET /healthz = %d, want 204", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run after cancel = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after cancel")
	}
}

func TestRunFailsWhenPortIsBusy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	err = run(t.Context(), envFrom(map[string]string{"ADDR": ln.Addr().String()}), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Errorf("run on busy port = %v, want a listen error", err)
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	if err := run(t.Context(), envFrom(map[string]string{"BASE_URL": "nope"}), io.Discard); err == nil {
		t.Error("run with bad BASE_URL = nil, want error")
	}
}
