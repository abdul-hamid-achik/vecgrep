package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

type sessionCloseProvider struct {
	closeErr error
	closed   bool
}

func (*sessionCloseProvider) Embed(context.Context, string) ([]float32, error) { return nil, nil }
func (*sessionCloseProvider) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return nil, nil
}
func (*sessionCloseProvider) Model() string                                 { return "test" }
func (*sessionCloseProvider) Dimensions() int                               { return 1 }
func (*sessionCloseProvider) Ping(context.Context) error                    { return nil }
func (*sessionCloseProvider) Warmup(context.Context) (time.Duration, error) { return 0, nil }
func (p *sessionCloseProvider) Close() error {
	p.closed = true
	return p.closeErr
}

type sessionVoidCloseProvider struct {
	sessionCloseProvider
	closed bool
}

func (p *sessionVoidCloseProvider) Close() {
	p.closed = true
}

func TestSessionCloseClosesProviderWithoutDatabase(t *testing.T) {
	closeErr := errors.New("close provider")
	provider := &sessionCloseProvider{closeErr: closeErr}
	session := &Session{Provider: provider}

	err := session.Close()
	if !provider.closed {
		t.Fatal("provider was not closed")
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("Close() error = %v, want provider close error", err)
	}
}

func TestSessionCloseInvokesVoidProviderCloser(t *testing.T) {
	provider := &sessionVoidCloseProvider{}
	session := &Session{Provider: provider}

	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}
	if !provider.closed {
		t.Fatal("provider Close() was not invoked")
	}
}
