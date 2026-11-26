package knowl

import (
	"errors"
	"testing"
)

const facadeTestToken = "facade-policy-token"

func TestFacadePolicyNormalization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		token     string
		api       FacadeConfig
		mcp       FacadeConfig
		web       WebConfig
		wantAPI   FacadeConfig
		wantMCP   FacadeConfig
		wantWeb   WebConfig
		wantError error
	}{
		{
			name:    "public read only defaults",
			wantAPI: FacadeConfig{AuthMode: AuthModePublic, AccessMode: AccessModeReadOnly},
			wantMCP: FacadeConfig{AuthMode: AuthModePublic, AccessMode: AccessModeReadOnly},
			wantWeb: WebConfig{AuthMode: AuthModePublic, AccessMode: AccessModeReadOnly},
		},
		{
			name:    "token keeps authentication but not write capability",
			token:   " " + facadeTestToken + " ",
			wantAPI: FacadeConfig{AuthMode: AuthModeToken, AccessMode: AccessModeReadOnly},
			wantMCP: FacadeConfig{AuthMode: AuthModeToken, AccessMode: AccessModeReadOnly},
			wantWeb: WebConfig{AuthMode: AuthModeToken, AccessMode: AccessModeReadOnly},
		},
		{
			name:    "independent policies",
			token:   facadeTestToken,
			api:     FacadeConfig{AuthMode: AuthModeToken, AccessMode: AccessModeReadWrite},
			mcp:     FacadeConfig{AuthMode: AuthModeToken, AccessMode: AccessModeReadOnly},
			web:     WebConfig{Enabled: true, AuthMode: AuthModePublic},
			wantAPI: FacadeConfig{AuthMode: AuthModeToken, AccessMode: AccessModeReadWrite},
			wantMCP: FacadeConfig{AuthMode: AuthModeToken, AccessMode: AccessModeReadOnly},
			wantWeb: WebConfig{Enabled: true, AuthMode: AuthModePublic, AccessMode: AccessModeReadOnly},
		},
		{
			name:      "public write rejected",
			api:       FacadeConfig{AuthMode: AuthModePublic, AccessMode: AccessModeReadWrite},
			wantError: ErrFacadeConfigInvalid,
		},
		{
			name:      "token without credential rejected",
			mcp:       FacadeConfig{AuthMode: AuthModeToken},
			wantError: ErrFacadeConfigInvalid,
		},
		{
			name:      "proxy is web only",
			token:     facadeTestToken,
			mcp:       FacadeConfig{AuthMode: AuthModeProxy},
			wantError: ErrFacadeConfigInvalid,
		},
		{
			name:      "enabled proxy requires credential",
			web:       WebConfig{Enabled: true, AuthMode: AuthModeProxy},
			wantError: ErrWebConfigInvalid,
		},
		{
			name:    "disabled proxy is inert",
			web:     WebConfig{AuthMode: AuthModeProxy},
			wantAPI: FacadeConfig{AuthMode: AuthModePublic, AccessMode: AccessModeReadOnly},
			wantMCP: FacadeConfig{AuthMode: AuthModePublic, AccessMode: AccessModeReadOnly},
			wantWeb: WebConfig{AuthMode: AuthModeProxy, AccessMode: AccessModeReadOnly},
		},
		{
			name:    "disabled token is inert",
			web:     WebConfig{AuthMode: AuthModeToken},
			wantAPI: FacadeConfig{AuthMode: AuthModePublic, AccessMode: AccessModeReadOnly},
			wantMCP: FacadeConfig{AuthMode: AuthModePublic, AccessMode: AccessModeReadOnly},
			wantWeb: WebConfig{AuthMode: AuthModeToken, AccessMode: AccessModeReadOnly},
		},
		{
			name:      "web write rejected",
			token:     facadeTestToken,
			web:       WebConfig{AccessMode: AccessModeReadWrite},
			wantError: ErrWebConfigInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultConfig()
			config.Workspace = t.TempDir()
			config.OperatorToken = test.token
			config.API = test.api
			config.MCP = test.mcp
			config.Web = test.web
			got, err := config.normalized()
			if !errors.Is(err, test.wantError) {
				t.Fatalf("normalized() error = %v, want %v", err, test.wantError)
			}
			if test.wantError != nil {
				return
			}
			if got.API != test.wantAPI || got.MCP != test.wantMCP || got.Web != test.wantWeb {
				t.Fatalf("policies = API %+v MCP %+v Web %+v, want API %+v MCP %+v Web %+v", got.API, got.MCP, got.Web, test.wantAPI, test.wantMCP, test.wantWeb)
			}
		})
	}
}
