package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/baldaworks/knowl/internal/httpapi/trustedrequest"
	"github.com/baldaworks/knowl/pkg/knowl"
)

type localWorkflowHost interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Handler() http.Handler
}

type localWorkflowHostCloser interface {
	Close() error
}

type localWorkflowSession struct {
	Host            localWorkflowHost
	ShutdownTimeout time.Duration
}

type localWorkflowSessionFactory func(context.Context) (localWorkflowSession, error)

var newLocalWorkflowSession = newProductionLocalWorkflowSession

type localWorkflowRequest struct {
	Method  string
	Path    string
	Body    []byte
	Headers http.Header
}

type localWorkflowResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

type localWorkflowRunner struct {
	newSession localWorkflowSessionFactory
}

func newLocalWorkflowRunner() *localWorkflowRunner {
	return &localWorkflowRunner{newSession: newLocalWorkflowSession}
}

func newProductionLocalWorkflowSession(ctx context.Context) (localWorkflowSession, error) {
	runtimeFactory, providerID, err := selectedRuntimeProvider(ctx)
	if err != nil {
		return localWorkflowSession{}, err
	}
	config, err := hostConfig(ctx)
	if err != nil {
		return localWorkflowSession{}, err
	}
	config.ListenAddr = loopbackListenAddr
	host, err := knowl.New(ctx, knowl.Options{
		Config:         config,
		RuntimeFactory: runtimeFactory,
		ProviderID:     providerID,
	})
	if err != nil {
		return localWorkflowSession{}, err
	}
	return localWorkflowSession{
		Host:            host,
		ShutdownTimeout: config.ShutdownTimeout,
	}, nil
}

func (runner *localWorkflowRunner) Execute(ctx context.Context, request localWorkflowRequest) (_ localWorkflowResponse, returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if runner == nil || runner.newSession == nil {
		return localWorkflowResponse{}, fmt.Errorf("local workflow session factory is required")
	}
	if request.Method == "" {
		return localWorkflowResponse{}, fmt.Errorf("workflow request method is required")
	}
	if request.Path == "" {
		return localWorkflowResponse{}, fmt.Errorf("workflow request path is required")
	}
	session, err := runner.newSession(ctx)
	if err != nil {
		return localWorkflowResponse{}, err
	}
	if session.Host == nil {
		return localWorkflowResponse{}, fmt.Errorf("local workflow host is required")
	}
	if session.ShutdownTimeout <= 0 {
		session.ShutdownTimeout = 10 * time.Second
	}
	if err := session.Host.Start(ctx); err != nil {
		startErr := fmt.Errorf("start local workflow host: %w", err)
		if closeErr := closeLocalWorkflowHost(session.Host); closeErr != nil {
			return localWorkflowResponse{}, errors.Join(startErr, fmt.Errorf("close local workflow host after start failure: %w", closeErr))
		}
		return localWorkflowResponse{}, startErr
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), session.ShutdownTimeout)
		defer cancel()
		if err := session.Host.Stop(stopCtx); err != nil {
			returnErr = fmt.Errorf("stop local workflow host: %w", err)
		}
	}()

	httpRequest := httptest.NewRequest(request.Method, "http://knowl"+request.Path, bytes.NewReader(request.Body))
	httpRequest = trustedrequest.Mark(httpRequest)
	for key, values := range request.Headers {
		for _, value := range values {
			httpRequest.Header.Add(key, value)
		}
	}
	response := httptest.NewRecorder()
	session.Host.Handler().ServeHTTP(response, httpRequest)
	return localWorkflowResponse{
		StatusCode: response.Code,
		Header:     response.Header().Clone(),
		Body:       response.Body.Bytes(),
	}, nil
}

func closeLocalWorkflowHost(host localWorkflowHost) error {
	closer, ok := host.(localWorkflowHostCloser)
	if !ok || closer == nil {
		return nil
	}
	return closer.Close()
}
