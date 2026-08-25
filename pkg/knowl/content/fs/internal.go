package fs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/baldaworks/knowl/pkg/knowl/types"
)

func sourceRefKey(source knowl.AcceptedSource) string {
	return source.Source.Adapter + ":" + source.Source.ID + "@" + source.Version.Version
}

type contentValidationError struct {
	target string
	rule   string
}

func (err *contentValidationError) Error() string {
	return err.SafeDetail() + ": " + ErrContentInvalid.Error()
}

func (err *contentValidationError) Unwrap() error { return ErrContentInvalid }

// SafeDetail exposes only the validated canonical target and stable rule. It
// deliberately excludes source content, absolute workspace paths, and adapter
// errors so operator-facing staging failures remain useful without leaking
// untrusted data.
func (err *contentValidationError) SafeDetail() string {
	return fmt.Sprintf("content validation failed for %q (%s)", err.target, err.rule)
}

func contentInvalidError(target, rule string) error {
	return &contentValidationError{target: target, rule: rule}
}

func digestBytes(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func token(value string) string {
	return digestBytes([]byte(value))[:32]
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func writeAtomic(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".knowl-tmp-")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() {
		_ = os.Remove(temporaryName)
	}()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}
