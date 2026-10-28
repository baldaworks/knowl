package searchtome

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
)

var ErrTEI = errors.New("local reference TEI unavailable or incompatible")

type TEIInfo struct {
	Model        string `json:"model_id"`
	Revision     string `json:"model_sha"`
	Version      string `json:"version"`
	DType        string `json:"model_dtype"`
	AutoTruncate *bool  `json:"auto_truncate"`
}

func validServerInfo(info TEIInfo, space app.EmbeddingSpace) bool {
	version := teiVersion
	if space == Gemma2Space {
		version = "sentence-transformers/6.1.0"
	} else if space != ReferenceSpace {
		return false
	}
	return info.Model == space.Model && info.Revision == space.Revision && info.Version == version && info.DType == teiFloat32 && info.AutoTruncate != nil && !*info.AutoTruncate
}

// LocalTEI verifies the reference server before any corpus text is sent.
func LocalTEI(ctx context.Context, endpoint string) (app.EmbeddingProvider, TEIInfo, error) {
	return LocalEmbeddingServer(ctx, endpoint, ReferenceSpace)
}

// LocalEmbeddingServer validates the selected pinned local inference server.
func LocalEmbeddingServer(ctx context.Context, endpoint string, space app.EmbeddingSpace) (app.EmbeddingProvider, TEIInfo, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != teiPath {
		return nil, TEIInfo{}, ErrTEI
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if host != localHost && (ip == nil || !ip.IsLoopback()) {
		return nil, TEIInfo{}, ErrTEI
	}
	// Avoid DNS ambiguity for localhost and ignore proxy configuration.
	if host == localHost {
		port := u.Port()
		if port == "" {
			port = "80"
		}
		u.Host = net.JoinHostPort("127.0.0.1", port)
	}
	infoURL := *u
	infoURL.Path = "/info"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, infoURL.String(), nil)
	if err != nil {
		return nil, TEIInfo{}, ErrTEI
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, TEIInfo{}, ErrTEI
	}
	defer func() { _ = response.Body.Close() }()
	var info TEIInfo
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&info) != nil || !validServerInfo(info, space) {
		return nil, TEIInfo{}, ErrTEI
	}
	p, err := provider.NewEmbeddingClient(provider.EmbeddingClientOptions{Endpoint: u.String(), Space: space})
	if err != nil {
		return nil, TEIInfo{}, ErrTEI
	}
	return localBatchProvider{provider: p}, info, nil
}

// LocalEndpoint rejects secrets and remote addresses without contacting them.
func LocalEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != teiPath || strings.TrimSpace(u.Host) != u.Host {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return u.Hostname() == localHost || (ip != nil && ip.IsLoopback())
}

// The reference TEI configuration accepts at most four inputs per request
// under its four-permit concurrency limit.
type localBatchProvider struct{ provider app.EmbeddingProvider }

func (b localBatchProvider) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	result := make([][]float32, 0, len(inputs))
	for start := 0; start < len(inputs); start += 4 {
		end := min(start+4, len(inputs))
		vectors, err := b.provider.Embed(ctx, inputs[start:end])
		if err != nil {
			return nil, err
		}
		result = append(result, vectors...)
	}
	return result, nil
}
