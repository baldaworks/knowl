package provider

import (
	"fmt"
	"io"

	adkagent "google.golang.org/adk/v2/agent"
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
	if maintainer.runtime == nil || maintainer.runtime.closer == nil {
		return nil
	}
	if err := maintainer.runtime.closer.Close(); err != nil {
		return fmt.Errorf("close maintainer provider")
	}
	return nil
}

func closeAgent(agent adkagent.Agent) {
	if closer, ok := agent.(io.Closer); ok {
		_ = closer.Close()
	}
}
