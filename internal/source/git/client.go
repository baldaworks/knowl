package git

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	gogit "github.com/go-git/go-git/v5"
	gogitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
)

// DefaultNetworkTimeout bounds network operations against the remote repository.
const DefaultNetworkTimeout = 30 * time.Second

// RemoteClient queries and interacts with remote Git repositories without executing local processes.
type RemoteClient struct {
	httpClient *http.Client
}

var installGitTransports sync.Once

type transferBudgetKey struct{}

type transferBudget struct {
	mu        sync.Mutex
	remaining int64
}

// NewRemoteClient constructs a RemoteClient with bounded network timeouts and SSRF controls.
func NewRemoteClient() *RemoteClient {
	dialer := &net.Dialer{
		Timeout:   DefaultNetworkTimeout,
		KeepAlive: 30 * time.Second,
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           safeDialContext(dialer),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	client := &RemoteClient{
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   DefaultNetworkTimeout,
			CheckRedirect: func(request *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many Git HTTPS redirects")
				}
				if request.URL.Scheme != "https" {
					return errors.New("git HTTPS redirect changed to an unsupported transport")
				}
				return nil
			},
		},
	}
	installGitTransports.Do(func() {
		httpsTransport := githttp.NewClient(client.httpClient)
		gitclient.InstallProtocol("https", limitingTransport{base: httpsTransport})
		for _, protocol := range []string{"ssh", "file"} {
			if base := gitclient.Protocols[protocol]; base != nil {
				gitclient.InstallProtocol(protocol, limitingTransport{base: base})
			}
		}
	})
	return client
}

// ListRemoteRefs queries remote repository references over HTTPS or SSH.
func (c *RemoteClient) ListRemoteRefs(ctx context.Context, config knowl.GitSourceConfig) ([]*plumbing.Reference, error) {
	auth, err := ResolveAuth(config)
	if err != nil {
		return nil, err
	}

	rem := gogit.NewRemote(memory.NewStorage(), &gogitconfig.RemoteConfig{
		Name: "origin",
		URLs: []string{config.Remote},
	})

	listOpts := &gogit.ListOptions{
		Auth: auth,
	}

	refs, err := rem.ListContext(ctx, listOpts)
	if err != nil {
		return nil, c.classifyTransportError(err, config.Remote)
	}
	return refs, nil
}

func withTransferLimit(ctx context.Context, limit int64) context.Context {
	return context.WithValue(ctx, transferBudgetKey{}, &transferBudget{remaining: limit})
}

type limitingTransport struct {
	base transport.Transport
}

func (t limitingTransport) NewUploadPackSession(endpoint *transport.Endpoint, auth transport.AuthMethod) (transport.UploadPackSession, error) {
	session, err := t.base.NewUploadPackSession(endpoint, auth)
	if err != nil {
		return nil, err
	}
	return limitingUploadPackSession{UploadPackSession: session}, nil
}

func (t limitingTransport) NewReceivePackSession(endpoint *transport.Endpoint, auth transport.AuthMethod) (transport.ReceivePackSession, error) {
	return t.base.NewReceivePackSession(endpoint, auth)
}

type limitingUploadPackSession struct {
	transport.UploadPackSession
}

func (s limitingUploadPackSession) UploadPack(ctx context.Context, request *packp.UploadPackRequest) (*packp.UploadPackResponse, error) {
	response, err := s.UploadPackSession.UploadPack(ctx, request)
	if err != nil {
		return nil, err
	}
	budget, _ := ctx.Value(transferBudgetKey{}).(*transferBudget)
	if budget == nil {
		return response, nil
	}
	return packp.NewUploadPackResponseWithPackfile(request, &limitedPackReader{ReadCloser: response, budget: budget}), nil
}

type limitedPackReader struct {
	io.ReadCloser
	budget *transferBudget
}

func (reader *limitedPackReader) Read(buffer []byte) (int, error) {
	reader.budget.mu.Lock()
	defer reader.budget.mu.Unlock()
	if reader.budget.remaining <= 0 {
		var probe [1]byte
		count, err := reader.ReadCloser.Read(probe[:])
		if count == 0 && errors.Is(err, io.EOF) {
			return 0, io.EOF
		}
		return 0, WrapClassified(ClassResourceLimit, ErrLimit, "Git pack transfer exceeded configured byte limit")
	}
	if int64(len(buffer)) > reader.budget.remaining {
		buffer = buffer[:reader.budget.remaining]
	}
	count, err := reader.ReadCloser.Read(buffer)
	reader.budget.remaining -= int64(count)
	return count, err
}

func (c *RemoteClient) classifyTransportError(err error, remote string) error {
	if err == nil {
		return nil
	}
	var classified ClassifiedError
	if errors.As(err, &classified) {
		return classified
	}

	msg := RedactString(err.Error())
	lower := strings.ToLower(msg)

	switch {
	case strings.Contains(lower, "authentication required"),
		strings.Contains(lower, "authorization failed"),
		strings.Contains(lower, "permission denied (publickey)"),
		strings.Contains(lower, "invalid auth"),
		strings.Contains(lower, "401 unauthorized"),
		strings.Contains(lower, "403 forbidden"):
		return WrapClassified(ClassAuthentication, err, fmt.Sprintf("authentication failed for %s: %s", RedactURL(remote), msg))

	case strings.Contains(lower, "blocked git destination"),
		strings.Contains(lower, "unsupported transport"):
		return WrapClassified(ClassUnsupportedTransport, err, "Git destination or transport is not permitted")

	case strings.Contains(lower, "host key"),
		strings.Contains(lower, "host verification"):
		return WrapClassified(ClassHostVerification, err, fmt.Sprintf("host verification failed for %s", RedactURL(remote)))

	case strings.Contains(lower, "no such host"),
		strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "i/o timeout"),
		strings.Contains(lower, "timeout"),
		strings.Contains(lower, "network is unreachable"),
		strings.Contains(lower, "repository not found"),
		strings.Contains(lower, "not found"):
		return WrapClassified(ClassReachability, err, fmt.Sprintf("remote repository unreachable at %s: %s", RedactURL(remote), msg))

	default:
		return WrapClassified(ClassReachability, err, fmt.Sprintf("git remote failure: %s", msg))
	}
}

func safeDialContext(dialer *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("blocked git destination: invalid address")
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, address := range addresses {
			ip := address.IP
			if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
				return nil, fmt.Errorf("blocked git destination: non-public address")
			}
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("blocked git destination: hostname has no addresses")
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].IP.String(), port))
	}
}
