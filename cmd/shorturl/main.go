// Command shorturl runs the URL shortener HTTP server with an in-memory store.
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pequin/go-shorturl/internal/httpapi"
	"github.com/pequin/go-shorturl/internal/links"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Getenv, os.Stdout)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "shorturl:", err)
		os.Exit(1)
	}
}

type config struct {
	addr            string
	baseURL         *url.URL
	requestTimeout  time.Duration
	shutdownTimeout time.Duration
}

func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{addr: cmp.Or(getenv("ADDR"), ":8080")}

	rawBase := cmp.Or(getenv("BASE_URL"), "http://localhost:8080")
	base, err := url.Parse(rawBase)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return config{}, fmt.Errorf("BASE_URL %q: want an absolute http(s) URL", rawBase)
	}
	cfg.baseURL = base

	if cfg.requestTimeout, err = parsePositiveDuration(getenv, "REQUEST_TIMEOUT", "5s"); err != nil {
		return config{}, err
	}
	if cfg.shutdownTimeout, err = parsePositiveDuration(getenv, "SHUTDOWN_TIMEOUT", "15s"); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func parsePositiveDuration(getenv func(string) string, key, fallback string) (time.Duration, error) {
	d, err := time.ParseDuration(cmp.Or(getenv(key), fallback))
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be positive, got %s", key, d)
	}
	return d, nil
}

func run(ctx context.Context, getenv func(string) string, logOutput io.Writer) error {
	cfg, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(logOutput, nil))

	api := httpapi.New(links.NewMemory(), cfg.baseURL, logger)
	srv := &http.Server{
		Handler:           http.TimeoutHandler(api.Handler(), cfg.requestTimeout, `{"error":"request timed out"}`),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		// Must exceed requestTimeout, otherwise the connection is cut before
		// TimeoutHandler can send its 503.
		WriteTimeout: cfg.requestTimeout + 5*time.Second,
		IdleTimeout:  60 * time.Second,
		ErrorLog:     slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	// Listening before starting the goroutine surfaces "address in use"
	// as a startup error instead of racing it against shutdown.
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	logger.Info("listening", slog.String("addr", ln.Addr().String()), slog.String("base_url", cfg.baseURL.String()))

	select {
	case err := <-serveErr:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
		logger.Info("shutting down", slog.Any("cause", context.Cause(ctx)))
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
