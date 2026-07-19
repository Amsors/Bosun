package config

import (
	"testing"
	"time"
)

func TestLoadTerminalDefaultsAndValidation(t *testing.T) {
	cfg, err := LoadTerminal()
	if err != nil {
		t.Fatalf("LoadTerminal() error = %v", err)
	}
	if cfg.WriteQueueCapacity != 64 || cfg.InputQueueCapacity != 64 ||
		cfg.MaxFrameBytes != 64<<10 || cfg.ActivityMinInterval != 15*time.Second {
		t.Fatalf("LoadTerminal() = %#v", cfg)
	}

	t.Setenv("BOSUN_API_TERMINAL_WRITE_QUEUE_CAPACITY", "0")
	if _, err := LoadTerminal(); err == nil {
		t.Fatal("LoadTerminal() accepted zero queue capacity")
	}
}
