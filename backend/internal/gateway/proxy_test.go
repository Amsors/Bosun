package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testPinger struct {
	err error
}

func (p testPinger) Ping(context.Context) error { return p.err }

func TestGatewayForwardsWithPlatformKeyAndExportsUsageMetrics(t *testing.T) {
	var upstreamAuthorization string
	var upstreamAPIKey string
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upstreamAuthorization = request.Header.Get("Authorization")
		upstreamAPIKey = request.Header.Get("X-Api-Key")
		body, _ := io.ReadAll(request.Body)
		upstreamBody = string(body)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":11,"output_tokens":7}}`))
	}))
	t.Cleanup(upstream.Close)

	var logs bytes.Buffer
	handler := newTestHandler(t, upstream.URL, &logs)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"messages":[{"content":"private prompt"}]}`))
	request.Header.Set("Authorization", "Bearer projected-token")
	request.Header.Set("X-Api-Key", "agent-controlled-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if upstreamAuthorization != "" {
		t.Fatalf("projected token reached upstream Authorization = %q", upstreamAuthorization)
	}
	if upstreamAPIKey != "platform-secret-key" {
		t.Fatalf("upstream X-Api-Key = %q", upstreamAPIKey)
	}
	if !strings.Contains(upstreamBody, "private prompt") {
		t.Fatalf("upstream body = %q", upstreamBody)
	}
	if strings.Contains(logs.String(), "private prompt") ||
		strings.Contains(logs.String(), "platform-secret-key") ||
		strings.Contains(logs.String(), "projected-token") {
		t.Fatalf("sensitive value appeared in logs: %s", logs.String())
	}

	metricsRequest := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsResponse := httptest.NewRecorder()
	handler.ServeHTTP(metricsResponse, metricsRequest)
	metricsBody := metricsResponse.Body.String()
	for _, wanted := range []string{
		`bosun_gateway_requests_total{provider="test-provider",status="200"} 1`,
		`bosun_gateway_usage_tokens_total{provider="test-provider",type="input"} 11`,
		`bosun_gateway_usage_tokens_total{provider="test-provider",type="output"} 7`,
	} {
		if !strings.Contains(metricsBody, wanted) {
			t.Fatalf("metrics missing %q:\n%s", wanted, metricsBody)
		}
	}
}

func TestGatewaySanitizesUpstreamErrors(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":"private prompt sk-real-provider-key"}`))
	}))
	t.Cleanup(upstream.Close)
	handler := newTestHandler(t, upstream.URL, io.Discard)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer projected-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
	body := response.Body.String()
	if !strings.Contains(body, "upstream_authentication_failed") ||
		strings.Contains(body, "private prompt") ||
		strings.Contains(body, "sk-real-provider-key") {
		t.Fatalf("gateway error body was not sanitized: %s", body)
	}
}

func TestGatewayFailsClosedWhenTokenReviewIsUnavailable(t *testing.T) {
	reviewer := &fakeTokenReviewer{err: errors.New("apiserver down")}
	authenticator := NewAuthenticator(reviewer, fakeSessionResolver{}, 200*time.Millisecond)
	metrics := NewMetrics()
	handler, err := NewHandler(
		HandlerConfig{
			UpstreamURL: "http://upstream.invalid", UpstreamAPIKey: "sk-xxxx",
			Provider: "test-provider", UpstreamAuthHeader: "X-Api-Key", UpstreamTimeout: time.Second,
		},
		authenticator,
		testPinger{},
		metrics,
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer projected-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), "token_review_unavailable") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestGatewayReadinessReflectsDatabase(t *testing.T) {
	handler := newTestHandlerWithPinger(t, "http://upstream.invalid", io.Discard, testPinger{err: errors.New("db down")})
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness status = %d", response.Code)
	}
}

func TestExtractTokenUsageFromStreamingResponse(t *testing.T) {
	body := []byte("event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":9,"output_tokens":1}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":6}}` + "\n\n")
	usage := extractTokenUsage(body)
	if usage.Input != 9 || usage.Output != 6 {
		t.Fatalf("usage = %#v", usage)
	}
}

func newTestHandler(t *testing.T, upstreamURL string, logOutput io.Writer) http.Handler {
	t.Helper()
	return newTestHandlerWithPinger(t, upstreamURL, logOutput, testPinger{})
}

func newTestHandlerWithPinger(
	t *testing.T,
	upstreamURL string,
	logOutput io.Writer,
	pinger testPinger,
) http.Handler {
	t.Helper()
	namespace := "bosun-u-123456789abc"
	authenticator := NewAuthenticator(
		&fakeTokenReviewer{response: validReview(namespace, testSessionID)},
		fakeSessionResolver{identity: validSessionIdentity(namespace, testSessionID)},
		time.Second,
	)
	handler, err := NewHandler(
		HandlerConfig{
			UpstreamURL: upstreamURL, UpstreamAPIKey: "platform-secret-key",
			Provider: "test-provider", UpstreamAuthHeader: "X-Api-Key", UpstreamTimeout: time.Second,
		},
		authenticator,
		pinger,
		NewMetrics(),
		slog.New(slog.NewJSONHandler(logOutput, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}
