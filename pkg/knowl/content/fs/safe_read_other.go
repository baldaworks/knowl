//go:build !linux && !darwin && !windows

package fs

import (
	"github.com/baldaworks/knowl/pkg/knowl/app"
	"os"
)

func openReadDirectory(string) (*os.File, error) { return nil, app.ErrOperatorWorkspaceUnavailable }
func openReadChild(*os.File, string, bool) (*os.File, error) {
	return nil, app.ErrOperatorWorkspaceUnavailable
}
