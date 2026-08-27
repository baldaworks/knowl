package provider

import (
	"context"
	"errors"
	"fmt"
	"io"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
)

// Close releases the provider agent. It is safe to call more than once.
func (maintainer *RuntimeMaintainer) Close() error {
	if maintainer == nil {
		return nil
	}
	maintainer.mu.Lock()
	defer maintainer.mu.Unlock()
	if maintainer.closed {
		return nil
	}
	maintainer.closed = true
	if maintainer.cancel != nil {
		defer maintainer.cancel()
	}
	if maintainer.runtime == nil {
		return nil
	}
	var closeErrors []error
	if maintainer.runtime.sessions != nil && maintainer.runtime.sessionID != "" {
		if err := maintainer.runtime.sessions.Delete(context.Background(), &session.DeleteRequest{
			AppName:   maintainerAppName,
			UserID:    maintainerUserID,
			SessionID: maintainer.runtime.sessionID,
		}); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("delete maintainer session: %w", err))
		}
	}
	if maintainer.runtime.closer != nil {
		if err := maintainer.runtime.closer.Close(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close maintainer provider: %w", err))
		}
	}
	return errors.Join(closeErrors...)
}

func closeAgent(agent adkagent.Agent) {
	if closer, ok := agent.(io.Closer); ok {
		_ = closer.Close()
	}
}
