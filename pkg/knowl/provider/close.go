package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
)

// Close releases the provider agent. It is safe to call more than once.
func (maintainer *RuntimeMaintainer) Close() error {
	if maintainer == nil {
		return nil
	}
	maintainer.mu <- struct{}{}
	defer func() { <-maintainer.mu }()
	maintainer.closed = true
	if maintainer.cancel != nil {
		maintainer.cancel()
	}
	var failures []error
	if maintainer.runtime != nil {
		if err := closeMaintainerRuntime(maintainer.runtime); err != nil {
			failures = append(failures, err)
		} else {
			maintainer.runtime = nil
		}
	}
	var pending []*maintainerRuntime
	for _, runtime := range maintainer.cleanup {
		if err := closeMaintainerRuntime(runtime); err != nil {
			failures = append(failures, err)
			pending = append(pending, runtime)
		}
	}
	maintainer.cleanup = pending
	return errors.Join(failures...)
}

func closeMaintainerRuntime(runtime *maintainerRuntime) error {
	var failures []error
	if runtime.sessions != nil && runtime.sessionID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := runtime.sessions.Delete(ctx, &session.DeleteRequest{AppName: maintainerAppName, UserID: maintainerUserID, SessionID: runtime.sessionID})
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("delete maintainer session: %w", err))
		} else {
			runtime.sessionID = ""
		}
	}
	if runtime.closer != nil {
		if err := runtime.closer.Close(); err != nil {
			failures = append(failures, fmt.Errorf("close maintainer provider: %w", err))
		} else {
			runtime.closer = nil
		}
	}
	return errors.Join(failures...)
}

// Failed setup retains any cleanup that failed, preventing another runtime build.
func (maintainer *RuntimeMaintainer) discardRuntime(runtime *maintainerRuntime) {
	if err := closeMaintainerRuntime(runtime); err != nil {
		maintainer.cleanup = append(maintainer.cleanup, runtime)
	}
}

func (maintainer *RuntimeMaintainer) discardAgent(agent adkagent.Agent) {
	closer, _ := agent.(io.Closer)
	maintainer.discardRuntime(&maintainerRuntime{closer: closer})
}
