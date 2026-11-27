package server

import (
	"context"
	"errors"
	"net/http"
	"os"

	"github.com/baldaworks/knowl/pkg/knowl/app"
)

const (
	notFoundCode           = "not_found"
	readLimitExceededCode  = "read_limit_exceeded"
	serviceUnavailableCode = "service_unavailable"
)

func writeServiceError(response http.ResponseWriter, err error) {
	status, class := classifyServiceError(err)
	writeHTTPError(response, status, class)
}

func classifyServiceError(err error) (int, string) {
	switch {
	case errors.Is(err, app.ErrMaintainerUnavailable):
		return http.StatusServiceUnavailable, "maintainer_unavailable"
	case errors.Is(err, os.ErrNotExist), errors.Is(err, app.ErrPageNotFound), errors.Is(err, app.ErrOperationNotFound):
		return http.StatusNotFound, notFoundCode
	case errors.Is(err, app.ErrQueryInvalid), errors.Is(err, app.ErrFilingInvalid):
		return http.StatusBadRequest, invalidRequestCode
	case errors.Is(err, app.ErrPageResultTooLarge), errors.Is(err, app.ErrPublicRetrieveResultTooLarge), errors.Is(err, app.ErrOperatorReadLimitExceeded):
		return http.StatusRequestEntityTooLarge, readLimitExceededCode
	case errors.Is(err, app.ErrSourceInvalid):
		return http.StatusBadRequest, "invalid_source_filter"
	case errors.Is(err, app.ErrProjection), errors.Is(err, app.ErrOperatorWorkspaceUnavailable), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return http.StatusServiceUnavailable, serviceUnavailableCode
	case errors.Is(err, app.ErrOperationNotApplyable):
		return http.StatusConflict, "operation_not_applyable"
	default:
		return http.StatusUnprocessableEntity, "operation_failed"
	}
}
