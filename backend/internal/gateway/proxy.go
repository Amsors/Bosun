package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const maxUsageCaptureBytes = 2 << 20

type Pinger interface {
	Ping(context.Context) error
}

type HandlerConfig struct {
	UpstreamURL        string
	UpstreamAPIKey     string
	Provider           string
	UpstreamAuthHeader string
	UpstreamAuthScheme string
	UpstreamTimeout    time.Duration
}

type Proxy struct {
	authenticator *Authenticator
	upstream      *url.URL
	apiKey        string
	provider      string
	authHeader    string
	authScheme    string
	timeout       time.Duration
	client        *http.Client
	metrics       *Metrics
	logger        *slog.Logger
}

func NewHandler(
	cfg HandlerConfig,
	authenticator *Authenticator,
	database Pinger,
	metrics *Metrics,
	logger *slog.Logger,
) (http.Handler, error) {
	upstream, err := url.Parse(cfg.UpstreamURL)
	if err != nil {
		return nil, fmt.Errorf("parse gateway upstream URL: %w", err)
	}
	proxy := &Proxy{
		authenticator: authenticator,
		upstream:      upstream,
		apiKey:        cfg.UpstreamAPIKey,
		provider:      cfg.Provider,
		authHeader:    cfg.UpstreamAuthHeader,
		authScheme:    cfg.UpstreamAuthScheme,
		timeout:       cfg.UpstreamTimeout,
		client: &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          64,
			MaxIdleConnsPerHost:   32,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		}},
		metrics: metrics,
		logger:  logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ok", "component": "gateway"})
	})
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		if err := database.Ping(ctx); err != nil {
			writeGatewayError(writer, http.StatusServiceUnavailable, "database_unavailable", "")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ready", "component": "gateway"})
	})
	mux.Handle("/metrics", promhttp.HandlerFor(metrics.Registry(), promhttp.HandlerOpts{}))
	mux.Handle("/v1/", proxy)
	return mux, nil
}

func (p *Proxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	started := time.Now()
	requestID := newRequestID()
	status := http.StatusInternalServerError
	reason := ""
	usage := tokenUsage{}
	sessionID := ""
	defer func() {
		elapsed := time.Since(started)
		p.metrics.Observe(p.provider, status, reason, elapsed, usage)
		p.logger.Info(
			"llm request completed",
			"request_id", requestID,
			"session_id", sessionID,
			"provider", p.provider,
			"status", status,
			"latency_ms", elapsed.Milliseconds(),
			"input_tokens", usage.Input,
			"output_tokens", usage.Output,
			"reason", reason,
		)
	}()
	writer.Header().Set("X-Bosun-Request-ID", requestID)

	token, ok := bearerToken(request.Header.Get("Authorization"))
	if !ok {
		status, reason = http.StatusUnauthorized, "invalid_token"
		writeGatewayError(writer, status, reason, requestID)
		return
	}
	identity, err := p.authenticator.Authenticate(request.Context(), token)
	if err != nil {
		status, reason = statusForIdentityError(err)
		writeGatewayError(writer, status, reason, requestID)
		return
	}
	sessionID = identity.SessionID
	if identity.ProviderMode != "platform" {
		status, reason = http.StatusForbidden, "provider_not_available"
		writeGatewayError(writer, status, reason, requestID)
		return
	}

	upstreamCtx, cancel := context.WithTimeout(request.Context(), p.timeout)
	defer cancel()
	upstreamRequest, err := p.newUpstreamRequest(upstreamCtx, request)
	if err != nil {
		status, reason = http.StatusBadGateway, "upstream_request_failed"
		writeGatewayError(writer, status, reason, requestID)
		return
	}
	response, err := p.client.Do(upstreamRequest)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(upstreamCtx.Err(), context.DeadlineExceeded) {
			status, reason = http.StatusGatewayTimeout, "upstream_timeout"
		} else {
			status, reason = http.StatusBadGateway, "upstream_unavailable"
		}
		writeGatewayError(writer, status, reason, requestID)
		return
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode >= http.StatusBadRequest {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		status = response.StatusCode
		reason = stableUpstreamReason(status)
		if retryAfter := response.Header.Get("Retry-After"); retryAfter != "" {
			writer.Header().Set("Retry-After", retryAfter)
		}
		writeGatewayError(writer, status, reason, requestID)
		return
	}

	copyResponseHeaders(writer.Header(), response.Header)
	writer.Header().Set("X-Bosun-Request-ID", requestID)
	status = response.StatusCode
	writer.WriteHeader(response.StatusCode)
	capture := &limitedBuffer{remaining: maxUsageCaptureBytes}
	if err := copyResponse(writer, response, capture); err != nil {
		reason = "response_interrupted"
		return
	}
	usage = extractTokenUsage(capture.Bytes())
}

func (p *Proxy) newUpstreamRequest(ctx context.Context, request *http.Request) (*http.Request, error) {
	target := *p.upstream
	target.Path = joinURLPath(p.upstream.Path, request.URL.Path)
	target.RawPath = ""
	target.RawQuery = request.URL.RawQuery
	upstreamRequest, err := http.NewRequestWithContext(ctx, request.Method, target.String(), request.Body)
	if err != nil {
		return nil, fmt.Errorf("create upstream request: %w", err)
	}
	upstreamRequest.ContentLength = request.ContentLength
	copyRequestHeaders(upstreamRequest.Header, request.Header)
	upstreamRequest.Header.Del("Authorization")
	upstreamRequest.Header.Del("X-Api-Key")
	upstreamRequest.Header.Del("Proxy-Authorization")
	value := p.apiKey
	if p.authScheme != "" {
		value = p.authScheme + " " + value
	}
	upstreamRequest.Header.Set(p.authHeader, value)
	upstreamRequest.Host = p.upstream.Host
	return upstreamRequest, nil
}

func copyResponse(writer http.ResponseWriter, response *http.Response, capture io.Writer) error {
	streaming := strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream")
	buffer := make([]byte, 32<<10)
	for {
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			_, _ = capture.Write(chunk)
			if _, err := writer.Write(chunk); err != nil {
				return fmt.Errorf("write gateway response: %w", err)
			}
			if streaming {
				if err := http.NewResponseController(writer).Flush(); err != nil {
					return fmt.Errorf("flush gateway response: %w", err)
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("read upstream response: %w", readErr)
		}
	}
}

func copyRequestHeaders(destination, source http.Header) {
	for name, values := range source {
		if isHopByHopHeader(name) ||
			strings.EqualFold(name, "Cookie") ||
			strings.EqualFold(name, "Forwarded") ||
			strings.HasPrefix(strings.ToLower(name), "x-forwarded-") ||
			strings.HasPrefix(strings.ToLower(name), "x-bosun-") {
			continue
		}
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}

func copyResponseHeaders(destination, source http.Header) {
	for name, values := range source {
		if isHopByHopHeader(name) || strings.EqualFold(name, "Server") {
			continue
		}
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}

func isHopByHopHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	default:
		return false
	}
}

func statusForIdentityError(err error) (int, string) {
	switch {
	case errors.Is(err, ErrInvalidToken):
		return http.StatusUnauthorized, "invalid_token"
	case errors.Is(err, ErrIdentityMismatch):
		return http.StatusForbidden, "identity_mismatch"
	case errors.Is(err, ErrSessionUnavailable):
		return http.StatusForbidden, "session_unavailable"
	case errors.Is(err, ErrTokenReviewFailed):
		return http.StatusServiceUnavailable, "token_review_unavailable"
	default:
		return http.StatusServiceUnavailable, "identity_lookup_unavailable"
	}
}

func stableUpstreamReason(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "upstream_invalid_request"
	case http.StatusUnauthorized, http.StatusForbidden:
		return "upstream_authentication_failed"
	case http.StatusTooManyRequests:
		return "upstream_rate_limited"
	default:
		if status >= http.StatusInternalServerError {
			return "upstream_unavailable"
		}
		return "upstream_request_failed"
	}
}

func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	return token, token != ""
}

func newRequestID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}

func writeGatewayError(writer http.ResponseWriter, status int, reason, requestID string) {
	if requestID != "" {
		writer.Header().Set("X-Bosun-Request-ID", requestID)
	}
	writeJSON(writer, status, map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    "api_error",
			"message": reason,
		},
	})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	remaining int
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	originalLength := len(value)
	if b.remaining <= 0 {
		return originalLength, nil
	}
	if len(value) > b.remaining {
		value = value[:b.remaining]
	}
	_, _ = b.buffer.Write(value)
	b.remaining -= len(value)
	return originalLength, nil
}

func (b *limitedBuffer) Bytes() []byte {
	return b.buffer.Bytes()
}

type tokenUsage struct {
	Input  int64
	Output int64
}

func extractTokenUsage(body []byte) tokenUsage {
	usage := tokenUsage{}
	var value any
	if json.Unmarshal(body, &value) == nil {
		findUsage(value, &usage)
		return usage
	}

	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64<<10), maxUsageCaptureBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		if json.Unmarshal([]byte(payload), &value) == nil {
			findUsage(value, &usage)
		}
	}
	return usage
}

func findUsage(value any, usage *tokenUsage) {
	switch typed := value.(type) {
	case map[string]any:
		if usageValue, ok := typed["usage"].(map[string]any); ok {
			usage.Input = max(usage.Input, numericField(usageValue, "input_tokens", "prompt_tokens"))
			usage.Output = max(usage.Output, numericField(usageValue, "output_tokens", "completion_tokens"))
		}
		for _, child := range typed {
			findUsage(child, usage)
		}
	case []any:
		for _, child := range typed {
			findUsage(child, usage)
		}
	}
}

func numericField(value map[string]any, names ...string) int64 {
	for _, name := range names {
		switch number := value[name].(type) {
		case float64:
			return int64(number)
		case json.Number:
			parsed, _ := strconv.ParseInt(number.String(), 10, 64)
			return parsed
		}
	}
	return 0
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
