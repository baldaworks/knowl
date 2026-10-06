package server

import (
	"bytes"
	"encoding/json"
	"net/url"
)

// Decode bounded JSON objects or forms only to distinguish forbidden scope
// selection from other unsupported bodies. No supplied body reaches a service.
func operatorBodyOverride(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		if key, ok := token.(string); ok && operatorOverrideKey(key) {
			return true
		}
	}
	if values, err := url.ParseQuery(string(body)); err == nil {
		for key := range values {
			if operatorOverrideKey(key) {
				return true
			}
		}
	}
	return false
}
