package runner

import (
	"errors"
	"testing"
)

// A runner with no way to make a model is a runner that will fail on the first
// document job, minutes into an import. New refuses instead.
func TestNewRefusesWithoutFactory(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, ErrNoFactory) {
		t.Fatalf("New(Config{}) error = %v, want ErrNoFactory", err)
	}
}
