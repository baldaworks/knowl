package server

import (
	"errors"
	"net/http"
	"os"

	"github.com/baldaworks/knowl/pkg/knowl/app"
)

func writeServiceError(response http.ResponseWriter, err error) {
	status, class := classifyServiceError(err)
	writeHTTPError(response, status, class)
}

func classifyServiceError(err error) (int, string) {
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, app.ErrPageNotFound), errors.Is(err, app.ErrOperationNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, app.ErrQueryInvalid), errors.Is(err, app.ErrFilingInvalid):
		return http.StatusBadRequest, "invalid_request"
	case errors.Is(err, app.ErrProjection):
		return http.StatusServiceUnavailable, "service_unavailable"
	case errors.Is(err, app.ErrOperationNotApplyable):
		return http.StatusConflict, "operation_not_applyable"
	default:
		return http.StatusUnprocessableEntity, "operation_failed"
	}
}
