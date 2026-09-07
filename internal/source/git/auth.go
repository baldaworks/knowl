package git

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh"
)

var gitSCPPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]+@[a-zA-Z0-9_.-]+:[a-zA-Z0-9_./~-]+$`)

// ResolveAuth returns go-git transport.AuthMethod configured for the Git source, or nil if unauthenticated.
func ResolveAuth(config knowl.GitSourceConfig) (transport.AuthMethod, error) {
	if strings.HasPrefix(config.Remote, "https://") {
		return resolveHTTPSAuth(config.Auth)
	}
	if strings.HasPrefix(config.Remote, "ssh://") || gitSCPPattern.MatchString(config.Remote) {
		return resolveSSHAuth(config)
	}
	return nil, nil
}

func resolveHTTPSAuth(auth knowl.GitAuthConfig) (transport.AuthMethod, error) {
	secret := ""
	if auth.SecretEnv != "" {
		secret = strings.TrimSpace(os.Getenv(auth.SecretEnv))
		if secret == "" {
			return nil, WrapClassified(ClassAuthentication, ErrAuthentication, fmt.Sprintf("environment variable %q is empty", auth.SecretEnv))
		}
	}
	if secret == "" {
		return nil, nil
	}
	// Many Git forges accept personal access token as password with a dummy username or token as username
	return &githttp.BasicAuth{
		Username: "token",
		Password: secret,
	}, nil
}

func resolveSSHAuth(config knowl.GitSourceConfig) (transport.AuthMethod, error) {
	var keyBytes []byte
	if config.Auth.KeyFile != "" {
		data, err := os.ReadFile(config.Auth.KeyFile)
		if err != nil {
			return nil, WrapClassified(ClassAuthentication, fmt.Errorf("read key file: %w", err), "cannot read ssh private key file")
		}
		keyBytes = data
	} else if config.Auth.SecretEnv != "" {
		val := os.Getenv(config.Auth.SecretEnv)
		if strings.TrimSpace(val) == "" {
			return nil, WrapClassified(ClassAuthentication, ErrAuthentication, fmt.Sprintf("environment variable %q is empty", config.Auth.SecretEnv))
		}
		keyBytes = []byte(val)
	}

	hostKeyCallback, err := buildHostKeyCallback(config.Remote, config.KnownHosts)
	if err != nil {
		return nil, err
	}

	user := "git"
	if strings.HasPrefix(config.Remote, "ssh://") {
		parsed, parseErr := url.Parse(config.Remote)
		if parseErr == nil && parsed.User != nil && parsed.User.Username() != "" {
			user = parsed.User.Username()
		}
	} else if gitSCPPattern.MatchString(config.Remote) {
		parts := strings.SplitN(config.Remote, "@", 2)
		if len(parts) == 2 && parts[0] != "" {
			user = parts[0]
		}
	}

	if len(keyBytes) == 0 {
		return nil, nil
	}

	publicKeys, err := gitssh.NewPublicKeys(user, keyBytes, "")
	if err != nil {
		return nil, WrapClassified(ClassAuthentication, err, "invalid ssh private key")
	}
	publicKeys.HostKeyCallback = hostKeyCallback
	return publicKeys, nil
}

func buildHostKeyCallback(remote string, knownHosts []string) (ssh.HostKeyCallback, error) {
	expectedHost, err := sshRemoteHost(remote)
	if err != nil {
		return nil, WrapClassified(ClassHostVerification, err, "invalid SSH host identity")
	}
	if len(knownHosts) == 0 {
		// Strict fail-closed when no host keys are configured (REQ-SEC-003)
		return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			return WrapClassified(ClassHostVerification, ErrHostVerification, fmt.Sprintf("no known host keys configured for host %s", hostname))
		}, nil
	}

	type trustedKey struct {
		host string
		key  ssh.PublicKey
	}
	trustedKeys := make([]trustedKey, 0, len(knownHosts))
	for _, entry := range knownHosts {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		host := expectedHost
		keyText := entry
		fields := strings.Fields(entry)
		if len(fields) >= 3 && !strings.HasPrefix(fields[0], "ssh-") && !strings.HasPrefix(fields[0], "ecdsa-") && !strings.HasPrefix(fields[0], "sk-") {
			host = fields[0]
			keyText = strings.Join(fields[1:], " ")
		}
		if host != expectedHost {
			return nil, WrapClassified(ClassHostVerification, ErrHostVerification, fmt.Sprintf("known host entry does not match %s", expectedHost))
		}
		pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(keyText))
		if err != nil {
			return nil, WrapClassified(ClassHostVerification, err, "invalid known_hosts entry in configuration")
		}
		trustedKeys = append(trustedKeys, trustedKey{host: host, key: pubKey})
	}

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		actualHost := hostname
		if parsed, _, splitErr := net.SplitHostPort(hostname); splitErr == nil {
			actualHost = parsed
		}
		actualHost = strings.Trim(actualHost, "[]")
		if !strings.EqualFold(actualHost, expectedHost) {
			return WrapClassified(ClassHostVerification, ErrHostVerification, "SSH host identity does not match configured remote")
		}
		keyBytes := key.Marshal()
		for _, trusted := range trustedKeys {
			if strings.EqualFold(trusted.host, expectedHost) && string(trusted.key.Marshal()) == string(keyBytes) {
				return nil
			}
		}
		return WrapClassified(ClassHostVerification, ErrHostVerification, fmt.Sprintf("untrusted host key for %s", hostname))
	}, nil
}

func sshRemoteHost(remote string) (string, error) {
	if strings.HasPrefix(remote, "ssh://") {
		parsed, err := url.Parse(remote)
		if err != nil || parsed.Hostname() == "" {
			return "", ErrHostVerification
		}
		return strings.ToLower(parsed.Hostname()), nil
	}
	parts := strings.SplitN(remote, "@", 2)
	if len(parts) != 2 {
		return "", ErrHostVerification
	}
	hostAndPath := strings.SplitN(parts[1], ":", 2)
	if len(hostAndPath) != 2 || hostAndPath[0] == "" {
		return "", ErrHostVerification
	}
	return strings.ToLower(hostAndPath[0]), nil
}
