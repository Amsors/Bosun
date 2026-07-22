package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAuthProxyReadsRotatedTokenForEveryRequest(t *testing.T) {
	var authorizations []string
	var apiKeys []string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorizations = append(authorizations, request.Header.Get("Authorization"))
		apiKeys = append(apiKeys, request.Header.Get("X-Api-Key"))
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	target, err := urlParse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("first-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(newHandler(target, tokenFile))
	t.Cleanup(proxy.Close)

	sendProxyRequest(t, proxy.URL, "untrusted-key")
	if err := os.WriteFile(tokenFile, []byte("rotated-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sendProxyRequest(t, proxy.URL, "another-untrusted-key")

	if got := strings.Join(authorizations, ","); got != "Bearer first-token,Bearer rotated-token" {
		t.Fatalf("upstream Authorization values = %q", got)
	}
	if got := strings.Join(apiKeys, ","); got != "," {
		t.Fatalf("untrusted X-Api-Key values reached gateway: %q", got)
	}
}

func TestAuthProxyRejectsMissingTokenWithoutCallingGateway(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls++
	}))
	t.Cleanup(upstream.Close)
	target, err := urlParse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(newHandler(target, filepath.Join(t.TempDir(), "missing")))
	t.Cleanup(proxy.Close)

	response, err := http.Get(proxy.URL + "/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "token_unavailable") {
		t.Fatalf("response = %d %s", response.StatusCode, body)
	}
	if calls != 0 {
		t.Fatalf("gateway calls = %d, want 0", calls)
	}
}

func TestHealthEndpointDoesNotRequireToken(t *testing.T) {
	target, err := urlParse("https://gateway.example")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	newHandler(target, filepath.Join(t.TempDir(), "missing")).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("health status = %d", response.Code)
	}
}

func TestDrainWaitsForActiveRequestAndRejectsNewWork(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(started) })
		<-release
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	target, err := urlParse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(newHandler(target, tokenFile))
	t.Cleanup(proxy.Close)

	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		response, requestErr := http.Get(proxy.URL + "/v1/messages")
		if requestErr == nil {
			_ = response.Body.Close()
		}
	}()
	<-started

	drainDone := make(chan *http.Response, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodPost, proxy.URL+"/__bosun/drain?timeout=2s", nil)
		response, _ := http.DefaultClient.Do(request)
		drainDone <- response
	}()
	time.Sleep(50 * time.Millisecond)
	response, err := http.Get(proxy.URL + "/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("new request during drain status = %d", response.StatusCode)
	}
	close(release)
	<-requestDone
	drainResponse := <-drainDone
	defer func() { _ = drainResponse.Body.Close() }()
	if drainResponse.StatusCode != http.StatusOK {
		t.Fatalf("drain status = %d", drainResponse.StatusCode)
	}
}

func sendProxyRequest(t *testing.T, baseURL, apiKey string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/messages", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer user-controlled")
	request.Header.Set("X-Api-Key", apiKey)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
}

func urlParse(raw string) (*url.URL, error) { return url.Parse(raw) }
