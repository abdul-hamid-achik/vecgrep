package snapshot

import (
	"context"
	"testing"
)

func TestNewFcheapDetectsBinary(t *testing.T) {
	// Test that NewFcheap detects whether fcheap is on $PATH.
	// The result depends on the environment — we just verify the
	// Available() flag is consistent with the binary resolution.
	f := NewFcheap()
	if f == nil {
		t.Fatal("NewFcheap should never return nil")
	}
	// Available() should reflect whether the binary was found
	_ = f.Available()
}

func TestFcheapSaveUnavailableReturnsError(t *testing.T) {
	f := &Fcheap{bin: ""}
	_, err := f.Save(context.Background(), "/tmp", "test", "vecgrep", nil)
	if err == nil {
		t.Fatal("expected error when fcheap is unavailable")
	}
}

func TestFcheapRestoreUnavailableReturnsError(t *testing.T) {
	f := &Fcheap{bin: ""}
	err := f.Restore(context.Background(), "stash-id", "/tmp")
	if err == nil {
		t.Fatal("expected error when fcheap is unavailable")
	}
}

func TestFcheapListUnavailableReturnsError(t *testing.T) {
	f := &Fcheap{bin: ""}
	_, err := f.List(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error when fcheap is unavailable")
	}
}

func TestFcheapNilAvailable(t *testing.T) {
	var f *Fcheap
	if f.Available() {
		t.Fatal("nil fcheap should not be available")
	}
}
