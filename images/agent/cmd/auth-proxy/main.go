package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"
)

const (
	defaultListenAddress = "127.0.0.1:8080"
	defaultTokenFile     = "/var/run/secrets/bosun/token"
	gatewayAudience      = "bosun-llm-gateway"
)

type tokenContextKey struct{}

type options struct {
	listenAddress string
	upstream      string
	tokenFile     string
	healthcheck   string
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	opts, err := parseOptions(args)
	if err != nil {
		slog.Error("invalid configuration", "reason", err)
		return 2
	}
	if opts.healthcheck != "" {
		return runHealthcheck(opts.healthcheck)
	}

	target, err := url.Parse(opts.upstream)
	if err != nil || target.Scheme == "" || target.Host == "" {
		slog.Error("invalid configuration", "reason", "upstream must be an absolute URL")
		return 2
	}

	handler := newHandler(target, opts.tokenFile)
	server := &http.Server{
		Addr:              opts.listenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errs := make(chan error, 1)
	go func() {
		slog.Info("auth proxy started", "address", opts.listenAddress, "audience", gatewayAudience)
		errs <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
	case err := <-errs:
		if !errors.Is(err, http.ErrServerClosed) {
			slog.Error("auth proxy stopped unexpectedly", "reason", err)
			return 1
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("auth proxy shutdown failed", "reason", err)
		return 1
	}
	return 0
}

func parseOptions(args []string) (options, error) {
	flags := flag.NewFlagSet("bosun-auth-proxy", flag.ContinueOnError)
	var opts options
	flags.StringVar(&opts.listenAddress, "listen", defaultListenAddress, "loopback listen address")
	flags.StringVar(&opts.upstream, "upstream", "", "bosun-gateway base URL")
	flags.StringVar(&opts.tokenFile, "token-file", defaultTokenFile, "projected ServiceAccount token file")
	flags.StringVar(&opts.healthcheck, "healthcheck", "", "check an auth proxy health URL and exit")
	if err := flags.Parse(args); err != nil {
		return options{}, fmt.Errorf("parse flags: %w", err)
	}
	if opts.healthcheck == "" && opts.upstream == "" {
		return options{}, errors.New("upstream is required")
	}
	return opts, nil
}

func newHandler(target *url.URL, tokenFile string) http.Handler {
	proxy := &httputil.ReverseProxy{
		Director: func(request *http.Request) {
			request.URL.Scheme = target.Scheme
			request.URL.Host = target.Host
			request.URL.Path = joinURLPath(target.Path, request.URL.Path)
			request.URL.RawPath = ""
			request.Host = target.Host
			request.Header.Del("Authorization")
			request.Header.Del("X-Api-Key")
			request.Header.Del("Proxy-Authorization")
			request.Header.Set("Authorization", "Bearer "+request.Context().Value(tokenContextKey{}).(string))
		},
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
		FlushInterval: -1,
		ErrorHandler: func(writer http.ResponseWriter, _ *http.Request, err error) {
			slog.Warn("gateway request failed", "reason", stableProxyReason(err))
			writeError(writer, http.StatusBadGateway, "gateway_unavailable")
		},
	}

	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/healthz" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]string{"status": "ok"})
			return
		}
		tokenBytes, err := os.ReadFile(tokenFile)
		if err != nil || strings.TrimSpace(string(tokenBytes)) == "" {
			slog.Warn("projected token is unavailable", "reason", "token_unavailable")
			writeError(writer, http.StatusServiceUnavailable, "token_unavailable")
			return
		}
		token := strings.TrimSpace(string(tokenBytes))
		proxy.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), tokenContextKey{}, token)))
	})
}

func joinURLPath(basePath, requestPath string) string {
	if basePath == "" || basePath == "/" {
		return requestPath
	}
	trailingSlash := strings.HasSuffix(requestPath, "/")
	joined := path.Join(basePath, requestPath)
	if trailingSlash && !strings.HasSuffix(joined, "/") {
		joined += "/"
	}
	return joined
}

func runHealthcheck(rawURL string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 1
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 1
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func writeError(writer http.ResponseWriter, status int, reason string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    "api_error",
			"message": reason,
		},
	})
}

func stableProxyReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "gateway_timeout"
	}
	return "gateway_unavailable"
}
