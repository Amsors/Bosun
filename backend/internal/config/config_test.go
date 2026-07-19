package config

import (
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name      string
		component Component
		env       map[string]string
		wantAddr  string
		wantErr   bool
	}{
		{
			name:      "api defaults",
			component: ComponentAPI,
			env:       map[string]string{"BOSUN_DATABASE_URL": "postgres://db/bosun"},
			wantAddr:  ":8080",
		},
		{
			name:      "gateway override",
			component: ComponentGateway,
			env: map[string]string{
				"BOSUN_DATABASE_URL":                     "postgres://db/bosun",
				"BOSUN_GATEWAY_UPSTREAM_URL":             "https://llm.example/v1",
				"BOSUN_GATEWAY_UPSTREAM_API_KEY":         "sk-xxxx",
				"BOSUN_GATEWAY_LISTEN_ADDRESS":           ":9000",
				"BOSUN_GATEWAY_SHUTDOWN_TIMEOUT":         "3s",
				"BOSUN_GATEWAY_READ_HEADER_TIMEOUT":      "2s",
				"BOSUN_GATEWAY_DATABASE_CONNECT_TIMEOUT": "1s",
			},
			wantAddr: ":9000",
		},
		{
			name:      "database required",
			component: ComponentAPI,
			wantErr:   true,
		},
		{
			name:      "positive timeout required",
			component: ComponentAPI,
			env: map[string]string{
				"BOSUN_DATABASE_URL":         "postgres://db/bosun",
				"BOSUN_API_SHUTDOWN_TIMEOUT": "0s",
			},
			wantErr: true,
		},
		{
			name:      "positive migration timeout required",
			component: ComponentAPI,
			env: map[string]string{
				"BOSUN_DATABASE_URL":                 "postgres://db/bosun",
				"BOSUN_API_DATABASE_MIGRATE_TIMEOUT": "0s",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys := []string{
				"BOSUN_DATABASE_URL", "BOSUN_LOG_LEVEL",
				"BOSUN_API_LISTEN_ADDRESS", "BOSUN_API_SHUTDOWN_TIMEOUT",
				"BOSUN_API_DATABASE_MIGRATE_TIMEOUT",
				"BOSUN_GATEWAY_LISTEN_ADDRESS", "BOSUN_GATEWAY_SHUTDOWN_TIMEOUT",
				"BOSUN_GATEWAY_READ_HEADER_TIMEOUT", "BOSUN_GATEWAY_DATABASE_CONNECT_TIMEOUT",
				"BOSUN_GATEWAY_UPSTREAM_URL", "BOSUN_GATEWAY_UPSTREAM_API_KEY",
				"BOSUN_GATEWAY_PROVIDER", "BOSUN_GATEWAY_UPSTREAM_AUTH_HEADER",
				"BOSUN_GATEWAY_UPSTREAM_AUTH_SCHEME", "BOSUN_GATEWAY_TOKEN_REVIEW_TIMEOUT",
				"BOSUN_GATEWAY_SESSION_LOOKUP_TIMEOUT", "BOSUN_GATEWAY_UPSTREAM_TIMEOUT",
			}
			for _, key := range keys {
				t.Setenv(key, "")
			}
			for key, value := range tt.env {
				t.Setenv(key, value)
			}

			got, err := Load(tt.component)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got.ListenAddress != tt.wantAddr {
				t.Fatalf("ListenAddress = %q, want %q", got.ListenAddress, tt.wantAddr)
			}
			if err == nil && got.ShutdownTimeout <= 0*time.Second {
				t.Fatal("ShutdownTimeout must be positive")
			}
			if err == nil && tt.component == ComponentAPI && got.DatabaseMigrateTimeout <= 0*time.Second {
				t.Fatal("DatabaseMigrateTimeout must be positive")
			}
			if err == nil && tt.component == ComponentGateway && got.Gateway.UpstreamURL == "" {
				t.Fatal("gateway upstream URL must be loaded")
			}
		})
	}
}

func TestLoadGatewayRejectsUnsafeUpstreamAndAuthConfiguration(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{
			name: "http upstream",
			env: map[string]string{
				"BOSUN_GATEWAY_UPSTREAM_URL":     "http://llm.example",
				"BOSUN_GATEWAY_UPSTREAM_API_KEY": "sk-xxxx",
			},
		},
		{
			name: "upstream credentials",
			env: map[string]string{
				"BOSUN_GATEWAY_UPSTREAM_URL":     "https://user:password@llm.example",
				"BOSUN_GATEWAY_UPSTREAM_API_KEY": "sk-xxxx",
			},
		},
		{
			name: "arbitrary auth header",
			env: map[string]string{
				"BOSUN_GATEWAY_UPSTREAM_URL":         "https://llm.example",
				"BOSUN_GATEWAY_UPSTREAM_API_KEY":     "sk-xxxx",
				"BOSUN_GATEWAY_UPSTREAM_AUTH_HEADER": "X-Custom-Key",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BOSUN_DATABASE_URL", "postgres://db/bosun")
			for _, key := range []string{
				"BOSUN_GATEWAY_UPSTREAM_URL", "BOSUN_GATEWAY_UPSTREAM_API_KEY",
				"BOSUN_GATEWAY_UPSTREAM_AUTH_HEADER", "BOSUN_GATEWAY_UPSTREAM_AUTH_SCHEME",
			} {
				t.Setenv(key, "")
			}
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			if _, err := Load(ComponentGateway); err == nil {
				t.Fatal("Load(ComponentGateway) expected an error")
			}
		})
	}
}
