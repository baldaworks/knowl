package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	Method string
	Path   string
	Body   []byte
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
			returnErr = errors.Join(returnErr, fmt.Errorf("stop local workflow host: %w", err))
		}
	}()

	httpRequest := httptest.NewRequest(request.Method, "http://knowl"+request.Path, bytes.NewReader(request.Body))
	httpRequest = trustedrequest.Mark(httpRequest)
	response := httptest.NewRecorder()
	session.Host.Handler().ServeHTTP(response, httpRequest)
	if err := waitForLocalIngest(ctx, session.Host.Handler(), request, response); err != nil {
		return localWorkflowResponse{}, err
	}
	return localWorkflowResponse{
		StatusCode: response.Code,
		Header:     response.Header().Clone(),
		Body:       response.Body.Bytes(),
	}, nil
}

func waitForLocalIngest(ctx context.Context, handler http.Handler, request localWorkflowRequest, response *httptest.ResponseRecorder) error {
	if request.Method != http.MethodPost || request.Path != "/v1/ingest" || response.Code != http.StatusOK {
		return nil
	}
	var submitted struct {
		OperationID string `json:"operation_id"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &submitted); err != nil {
		return fmt.Errorf("decode local ingest submission: %w", err)
	}
	if submitted.Status != "queued" || submitted.OperationID == "" {
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		operationRequest := httptest.NewRequest(http.MethodGet, "http://knowl/v1/operations/"+url.PathEscape(submitted.OperationID), nil)
		operationResponse := httptest.NewRecorder()
		handler.ServeHTTP(operationResponse, operationRequest)
		if operationResponse.Code != http.StatusOK {
			return fmt.Errorf("read local ingest operation: HTTP %d", operationResponse.Code)
		}
		var operation struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(operationResponse.Body.Bytes(), &operation); err != nil {
			return fmt.Errorf("decode local ingest operation: %w", err)
		}
		switch operation.Status {
		case "completed":
			response.Body.Reset()
			if err := json.NewEncoder(response.Body).Encode(struct {
				OperationID string `json:"operation_id"`
				Status      string `json:"status"`
			}{OperationID: submitted.OperationID, Status: operation.Status}); err != nil {
				return fmt.Errorf("encode completed local ingest: %w", err)
			}
			return nil
		case "failed":
			return fmt.Errorf("local ingest operation %q failed", submitted.OperationID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func closeLocalWorkflowHost(host localWorkflowHost) error {
	closer, ok := host.(localWorkflowHostCloser)
	if !ok || closer == nil {
		return nil
	}
	return closer.Close()
}
