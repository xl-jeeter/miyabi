package cd2

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ppxb/miyabi/internal/drive/cd2/gen"
	"github.com/ppxb/miyabi/internal/netx"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

const (
	// callTimeout bounds control-plane calls. Directory listings of large
	// folders need more than the usual API budget, media transfers do not use it.
	callTimeout = time.Minute
	// tokenExpiryMargin renews an issued JWT before it lapses mid-request.
	tokenExpiryMargin = 30 * time.Second
)

// Config describes how to reach one CloudDrive2 instance.
type Config struct {
	// Endpoint is the gRPC address, optionally with a scheme. A scheme of
	// "https" enables TLS; a bare host:port or "http" talks plaintext.
	Endpoint string
	// Token is an existing CloudDrive2 API token. When empty, Username and
	// Password are exchanged for a JWT on first use.
	Token string
	// Username and Password authenticate against CloudDrive2 itself.
	Username string
	Password string
	// Account pins the cloud account served by miyabi as "<name>:<userName>",
	// for example "115open:80005515". It is required on instances that hold
	// more than one account of the same cloud.
	Account string
}

// client owns the CloudDrive2 gRPC connection and its authorization state.
// gRPC reconnects on its own, so one connection lives for the process.
type client struct {
	api      gen.CloudDriveFileSrvClient
	conn     *grpc.ClientConn
	media    *http.Client
	scheme   string
	host     string
	username string
	password string

	tokens struct {
		sync.Mutex
		token   string
		expires time.Time
	}
}

func newClient(cfg Config) (*client, error) {
	address := strings.TrimSpace(cfg.Endpoint)
	if address == "" {
		return nil, ErrMissingEndpoint
	}
	scheme, host, useTLS := splitEndpoint(address)
	if cfg.Token == "" && (cfg.Username == "" || cfg.Password == "") {
		return nil, ErrTokenRequired
	}

	options := []grpc.DialOption{grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(64 << 20))}
	if useTLS {
		options = append(options, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})))
	} else {
		options = append(options, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	conn, err := grpc.NewClient(host, options...)
	if err != nil {
		return nil, fmt.Errorf("connect CloudDrive2 at %s: %w", host, err)
	}

	transport := netx.NewTransport(nil)
	transport.ResponseHeaderTimeout = callTimeout
	return &client{
		api:      gen.NewCloudDriveFileSrvClient(conn),
		conn:     conn,
		media:    &http.Client{Transport: transport},
		scheme:   scheme,
		host:     host,
		username: cfg.Username,
		password: cfg.Password,
	}, nil
}

// splitEndpoint turns "https://host:8443" into a gRPC target and the HTTP
// origin later used to expand download URL paths.
func splitEndpoint(address string) (scheme, host string, useTLS bool) {
	scheme, host = "http", address
	if parsed, err := url.Parse(address); err == nil && parsed.Host != "" {
		scheme, host = strings.ToLower(parsed.Scheme), parsed.Host
	} else if _, _, err := net.SplitHostPort(address); err == nil {
		scheme, host = "http", address
	} else {
		// A bare host keeps gRPC's default port expectations; TLS stays off.
		host = address
	}
	return scheme, host, scheme == "https"
}

// configureToken seeds the configured API token. Credentials are only
// exchanged on the first call that needs them.
func (c *client) configureToken(token string) {
	if token == "" {
		return
	}
	c.tokens.Lock()
	c.tokens.token = token
	c.tokens.Unlock()
}

// authorize attaches the bearer token to an outgoing call. CloudDrive2 accepts
// a long-lived API token or a short-lived JWT obtained from credentials.
func (c *client) authorize(ctx context.Context) (context.Context, context.CancelFunc, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(metadata.AppendToOutgoingContext(ctx,
		"authorization", "Bearer "+token), callTimeout)
	return ctx, cancel, nil
}

func (c *client) token(ctx context.Context) (string, error) {
	c.tokens.Lock()
	cached, expires := c.tokens.token, c.tokens.expires
	c.tokens.Unlock()
	if cached != "" && (expires.IsZero() || time.Now().Add(tokenExpiryMargin).Before(expires)) {
		return cached, nil
	}
	if c.username == "" || c.password == "" {
		return "", ErrTokenRequired
	}

	// GetToken requires no authorization of its own.
	request, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	issued, err := c.api.GetToken(request, &gen.GetTokenRequest{UserName: c.username, Password: c.password})
	if err != nil {
		return "", fmt.Errorf("authenticate CloudDrive2: %w", err)
	}
	if !issued.GetSuccess() || issued.GetToken() == "" {
		message := issued.GetErrorMessage()
		if message == "" {
			message = "CloudDrive2 rejected the configured credentials"
		}
		return "", ErrTokenRequired
	}

	c.tokens.Lock()
	c.tokens.token = issued.GetToken()
	c.tokens.expires = issued.GetExpiration().AsTime()
	c.tokens.Unlock()
	return issued.GetToken(), nil
}

func (c *client) close() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
	if c.media != nil {
		c.media.CloseIdleConnections()
	}
}

// mediaURL expands the download URL path returned by GetDownloadUrlPath. The
// placeholders are documented on the RPC itself.
func (c *client) mediaURL(path string, preview bool) (string, error) {
	expanded := strings.ReplaceAll(path, "{SCHEME}", c.scheme)
	expanded = strings.ReplaceAll(expanded, "{HOST}", c.host)
	expanded = strings.ReplaceAll(expanded, "{PREVIEW}", fmt.Sprint(preview))
	if !strings.HasPrefix(expanded, "/") {
		// A complete URL from a future CloudDrive2 version needs no assembly.
		return expanded, nil
	}
	return c.scheme + "://" + c.host + expanded, nil
}
