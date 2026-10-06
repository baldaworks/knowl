//go:build !linux && !darwin && !windows

package fs

import (
	"os"

	"github.com/baldaworks/knowl/pkg/knowl/app"
)

func operatorFileIdentity(*os.File) (string, error) { return "", app.ErrOperatorWorkspaceUnavailable }
