package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	operatorCatalogEndpoint    = "catalogs"
	operatorPagesEndpoint      = "pages"
	operatorOperationsEndpoint = "operations"
	maxOperatorCursorBytes     = 8 << 10
	maxOperatorQueryBytes      = 16 << 10
)

type operatorCursorPayload struct {
	Version         int    `json:"version"`
	Endpoint        string `json:"endpoint"`
	Scope           string `json:"scope"`
	Filter          string `json:"filter"`
	Limit           int    `json:"limit"`
	Key             string `json:"key"`
	SnapshotVersion string `json:"snapshot_version,omitempty"`
}
type operatorCursorEnvelope struct {
	Payload   operatorCursorPayload `json:"payload"`
	Signature string                `json:"signature"`
}

func (service *OperatorService) listOptions(ctx context.Context, endpoint, filter string, options OperatorListOptions) (OperatorReadOptions, error) {
	if err := contextErr(ctx); err != nil {
		return OperatorReadOptions{}, err
	}
	if len(options.Cursor) > maxOperatorCursorBytes {
		return OperatorReadOptions{}, ErrOperatorCursorInvalid
	}
	if len(options.Cursor)+len(filter) > maxOperatorQueryBytes {
		return OperatorReadOptions{}, ErrOperatorInvalidRequest
	}
	if options.Limit < 0 || options.Limit > 100 {
		return OperatorReadOptions{}, ErrOperatorLimitInvalid
	}
	if options.Limit == 0 {
		options.Limit = 50
	}
	normalized := OperatorReadOptions{Limit: options.Limit, ReadLimits: service.limits}
	if options.Cursor != "" {
		position, err := service.decodeCursor(endpoint, filter, options.Limit, options.Cursor)
		if err != nil {
			return OperatorReadOptions{}, err
		}
		normalized.Continuation = position
	}
	return normalized, nil
}

func (service *OperatorService) encodeCursor(endpoint, filter string, limit int, position OperatorContinuation) (string, error) {
	payload := operatorCursorPayload{Version: 1, Endpoint: endpoint, Scope: operatorFingerprint(string(service.scope)), Filter: operatorFingerprint(filter), Limit: limit, Key: position.Key, SnapshotVersion: position.SnapshotVersion}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", ErrOperatorWorkspaceUnavailable
	}
	wire := operatorCursorEnvelope{Payload: payload, Signature: base64.RawURLEncoding.EncodeToString(service.signCursor(data))}
	data, err = json.Marshal(wire)
	if err != nil {
		return "", ErrOperatorWorkspaceUnavailable
	}
	if base64.RawURLEncoding.EncodedLen(len(data)) > maxOperatorCursorBytes {
		return "", ErrOperatorReadLimitExceeded
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func (service *OperatorService) decodeCursor(endpoint, filter string, limit int, cursor string) (OperatorContinuation, error) {
	data, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	if err != nil {
		return OperatorContinuation{}, ErrOperatorCursorInvalid
	}
	var wire operatorCursorEnvelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return OperatorContinuation{}, ErrOperatorCursorInvalid
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return OperatorContinuation{}, ErrOperatorCursorInvalid
	}
	payload, err := json.Marshal(wire.Payload)
	if err != nil {
		return OperatorContinuation{}, ErrOperatorCursorInvalid
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(wire.Signature)
	if err != nil || !hmac.Equal(signature, service.signCursor(payload)) {
		return OperatorContinuation{}, ErrOperatorCursorInvalid
	}
	value := wire.Payload
	if value.Version != 1 || value.Endpoint != endpoint || value.Scope != operatorFingerprint(string(service.scope)) || value.Filter != operatorFingerprint(filter) || value.Limit != limit || !validOpaque(value.Key, maxCursorBytes, false) {
		return OperatorContinuation{}, ErrOperatorCursorInvalid
	}
	if (endpoint == operatorPagesEndpoint || endpoint == operatorCatalogEndpoint) && !validExecutionDigest(value.SnapshotVersion) {
		return OperatorContinuation{}, ErrOperatorCursorInvalid
	}

	if endpoint == operatorOperationsEndpoint {
		if _, err := DecodeOperatorOperationPosition(value.Key); err != nil {
			return OperatorContinuation{}, ErrOperatorCursorInvalid
		}
	}
	return OperatorContinuation{Key: value.Key, SnapshotVersion: value.SnapshotVersion}, nil
}
func (service *OperatorService) signCursor(data []byte) []byte {
	mac := hmac.New(sha256.New, service.cursorKey[:])
	_, _ = mac.Write(data)
	return mac.Sum(nil)
}
func operatorFingerprint(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// OperatorOperationPosition is the immutable durable operation creation tuple.
// Stores use the shared codec rather than selecting mutable status/update keys.
type OperatorOperationPosition struct {
	CreatedAt   time.Time         `json:"created_at"`
	OperationID knowl.OperationID `json:"operation_id"`
}

// EncodeOperatorOperationPosition creates the backend continuation key.
func EncodeOperatorOperationPosition(position OperatorOperationPosition) (string, error) {
	if !validOperatorPosition(position) {
		return "", ErrOperatorInvalidRequest
	}
	data, err := json.Marshal(position)
	if err != nil || len(data) > maxCursorBytes {
		return "", ErrOperatorInvalidRequest
	}
	return string(data), nil
}

// DecodeOperatorOperationPosition decodes a verified backend key, not a public cursor.
func DecodeOperatorOperationPosition(key string) (OperatorOperationPosition, error) {
	if !validOpaque(key, maxCursorBytes, false) {
		return OperatorOperationPosition{}, ErrOperatorCursorInvalid
	}
	var position OperatorOperationPosition
	decoder := json.NewDecoder(strings.NewReader(key))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&position); err != nil {
		return OperatorOperationPosition{}, ErrOperatorCursorInvalid
	}
	if err := decoder.Decode(new(any)); err != io.EOF || !validOperatorPosition(position) {
		return OperatorOperationPosition{}, ErrOperatorCursorInvalid
	}
	return position, nil
}

func validOperatorPosition(position OperatorOperationPosition) bool {
	return !position.CreatedAt.IsZero() && validOpaque(string(position.OperationID), maxCursorBytes, false)
}
