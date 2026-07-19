package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// TerminalConfig contains bounded WebSocket and activity settings for the API
// terminal proxy. Every size is intentionally finite to prevent a slow browser
// or exec stream from growing backend memory without limit.
type TerminalConfig struct {
	WriteQueueCapacity  int
	InputQueueCapacity  int
	MaxFrameBytes       int64
	WriteTimeout        time.Duration
	PongTimeout         time.Duration
	PingInterval        time.Duration
	ActivityMinInterval time.Duration
}

func LoadTerminal() (TerminalConfig, error) {
	cfg := TerminalConfig{
		WriteQueueCapacity:  64,
		InputQueueCapacity:  64,
		MaxFrameBytes:       64 << 10,
		WriteTimeout:        10 * time.Second,
		PongTimeout:         60 * time.Second,
		PingInterval:        30 * time.Second,
		ActivityMinInterval: 15 * time.Second,
	}
	var err error
	if cfg.WriteQueueCapacity, err = positiveInt("BOSUN_API_TERMINAL_WRITE_QUEUE_CAPACITY", cfg.WriteQueueCapacity); err != nil {
		return TerminalConfig{}, err
	}
	if cfg.InputQueueCapacity, err = positiveInt("BOSUN_API_TERMINAL_INPUT_QUEUE_CAPACITY", cfg.InputQueueCapacity); err != nil {
		return TerminalConfig{}, err
	}
	if cfg.MaxFrameBytes, err = positiveInt64("BOSUN_API_TERMINAL_MAX_FRAME_BYTES", cfg.MaxFrameBytes); err != nil {
		return TerminalConfig{}, err
	}
	if cfg.WriteTimeout, err = duration("BOSUN_API_TERMINAL_WRITE_TIMEOUT", cfg.WriteTimeout); err != nil {
		return TerminalConfig{}, err
	}
	if cfg.PongTimeout, err = duration("BOSUN_API_TERMINAL_PONG_TIMEOUT", cfg.PongTimeout); err != nil {
		return TerminalConfig{}, err
	}
	if cfg.PingInterval, err = duration("BOSUN_API_TERMINAL_PING_INTERVAL", cfg.PingInterval); err != nil {
		return TerminalConfig{}, err
	}
	if cfg.ActivityMinInterval, err = duration("BOSUN_API_TERMINAL_ACTIVITY_MIN_INTERVAL", cfg.ActivityMinInterval); err != nil {
		return TerminalConfig{}, err
	}
	if cfg.PingInterval >= cfg.PongTimeout {
		return TerminalConfig{}, fmt.Errorf("BOSUN_API_TERMINAL_PING_INTERVAL must be shorter than BOSUN_API_TERMINAL_PONG_TIMEOUT")
	}
	return cfg, nil
}

func positiveInt64(key string, fallback int64) (int64, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}
	return value, nil
}
