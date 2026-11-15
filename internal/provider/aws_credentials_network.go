package provider

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/endpointcreds"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/signin"
	"github.com/aws/aws-sdk-go-v2/service/sso"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/auth/bearer"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"vista/internal/network"
)

type awsMetadataRequestKey struct{}

const awsContainerService = "endpoint-credentials"

func isAWSMetadataRequest(r *http.Request) bool {
	source, _ := r.Context().Value(awsMetadataRequestKey{}).(string)
	return source != ""
}

// The CA resolver requires a native BuildableClient, while global middleware
// cannot inspect a generated service's final HTTP settings. Keep native config
// loading and obtain each service's settings from its SDK constructor at egress.
func bedrockAWSConfig(ctx context.Context, region string) (aws.Config, func(), error) {
	if _, err := network.ConnectionPolicy(ctx); err != nil {
		return aws.Config{}, func() {}, err
	}
	native := awshttp.NewBuildableClient().WithTransportOptions(func(t *http.Transport) {
		// Defaults-mode resolution replaces DialContext. Proxy survives those clones
		// and rejects an omitted dispatcher before a socket is opened.
		t.Proxy = func(*http.Request) (*url.URL, error) {
			return nil, errors.New("AWS credential request bypassed Vista HTTP dispatcher")
		}
	})
	transportConfig, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region), config.WithHTTPClient(native),
		config.WithCredentialsProvider(aws.AnonymousCredentials{}),
		config.WithBearerAuthTokenProvider(bearer.StaticTokenProvider{}),
		config.WithDefaultsMode(aws.DefaultsModeLegacy),
	)
	if err != nil {
		return aws.Config{}, func() {}, err
	}
	dispatch := &awsCredentialNetwork{operation: ctx, config: transportConfig, clients: make(map[string]*network.RuntimeIdentityClient)}
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region), config.WithHTTPClient(native),
		config.WithAPIOptions([]func(*middleware.Stack) error{markAWSMetadataRequests, awsCredentialDispatch(ctx, dispatch)}),
		config.WithEndpointCredentialOptions(func(o *endpointcreds.Options) {
			o.APIOptions = append(o.APIOptions, func(stack *middleware.Stack) error {
				return stack.Initialize.Add(middleware.InitializeMiddlewareFunc("VistaContainerIdentity", func(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
					return next.HandleInitialize(context.WithValue(ctx, awsMetadataRequestKey{}, awsContainerService), in)
				}), middleware.Before)
			})
		}),
	)
	dispatch.mu.Lock()
	dispatch.config = cfg
	dispatch.mu.Unlock()
	return cfg, dispatch.CloseIdleConnections, err
}

type awsCredentialNetwork struct {
	operation context.Context
	mu        sync.Mutex
	config    aws.Config
	clients   map[string]*network.RuntimeIdentityClient
}

func (n *awsCredentialNetwork) Do(r *http.Request) (*http.Response, error) {
	service := middleware.GetServiceID(r.Context())
	if source, _ := r.Context().Value(awsMetadataRequestKey{}).(string); source != "" {
		service = source
	}
	client, err := n.client(service, awsmiddleware.GetRegion(r.Context()))
	if err != nil {
		if r.Body != nil {
			r.Body.Close()
		}
		return nil, err
	}
	return client.Do(r)
}

func (n *awsCredentialNetwork) client(service, region string) (*network.RuntimeIdentityClient, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	key := service + "\x00" + region
	if client := n.clients[key]; client != nil {
		return client, nil
	}
	cfg := n.config.Copy()
	if region != "" {
		cfg.Region = region
	}
	native, err := awsCredentialServiceHTTP(cfg, service)
	if err != nil {
		return nil, err
	}
	client, err := awsCredentialHTTPClient(n.operation, native)
	if err != nil {
		return nil, err
	}
	n.clients[key] = client
	return client, nil
}

func (n *awsCredentialNetwork) CloseIdleConnections() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, client := range n.clients {
		client.CloseIdleConnections()
	}
}

// SDK constructors resolve defaults mode (including auto and the service's
// actual region) and service read settings. Do not duplicate their tables.
func awsCredentialServiceHTTP(cfg aws.Config, service string) (*awshttp.BuildableClient, error) {
	var client aws.HTTPClient
	switch service {
	case sts.ServiceID:
		client = sts.NewFromConfig(cfg).Options().HTTPClient
	case sso.ServiceID:
		client = sso.NewFromConfig(cfg).Options().HTTPClient
	case ssooidc.ServiceID:
		client = ssooidc.NewFromConfig(cfg).Options().HTTPClient
	case signin.ServiceID:
		client = signin.NewFromConfig(cfg).Options().HTTPClient
	case imds.ServiceID:
		var options *imds.Options
		// New specializes HTTPClient after running its options callbacks. Retain the
		// options until construction completes to observe that final SDK client.
		imds.NewFromConfig(cfg, func(o *imds.Options) { options = o })
		client = options.HTTPClient
	case awsContainerService:
		client = cfg.HTTPClient
	default:
		return nil, fmt.Errorf("unsupported AWS credential HTTP service %q", service)
	}
	native, ok := client.(*awshttp.BuildableClient)
	if !ok {
		return nil, errors.New("AWS credential HTTP settings are not available")
	}
	return native, nil
}

func awsCredentialHTTPClient(ctx context.Context, native *awshttp.BuildableClient) (*network.RuntimeIdentityClient, error) {
	options := *native.Freeze().(*http.Client)
	leaf := native.GetTransport()
	// Keep the application's native dial hook, including fixture/platform hooks,
	// inside the SDK-resolved connect budget.
	if base, ok := http.DefaultTransport.(*http.Transport); ok && base.DialContext != nil {
		dial := base.DialContext
		timeout := native.GetDialer().Timeout
		leaf.DialContext = func(ctx context.Context, kind, address string) (net.Conn, error) {
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			return dial(ctx, kind, address)
		}
	}
	// Freeze hides its leaf behind an unexported wrapper without Unwrap.
	// GetTransport omits the SDK's read-inactivity wrapper, so bridge only that
	// missing hook using its exported timeout setting and error classification.
	if timeout, set := native.GetReadTimeout(); set && timeout > 0 {
		dial := leaf.DialContext
		leaf.DialContext = func(ctx context.Context, kind, address string) (net.Conn, error) {
			conn, err := dial(ctx, kind, address)
			if err != nil {
				return nil, err
			}
			return &awsCredentialReadConn{Conn: conn, timeout: timeout}, nil
		}
	}
	options.Transport = leaf
	client, err := network.ClientFor(ctx, options, network.ConnectionScope)
	if err != nil {
		return nil, err
	}
	guarded, err := network.NewRuntimeIdentityClient(ctx, client, isAWSMetadataRequest)
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	return guarded, nil
}

// Read-inactivity adapter adapted from AWS SDK for Go v2 v1.47.1,
// aws/transport/http/timeouts.go (Apache-2.0). Original notices are retained
// under licenses/aws-sdk-go-v2/. The SDK owns timeout selection and errors.
type awsCredentialReadConn struct {
	net.Conn
	timeout time.Duration
}

func (c *awsCredentialReadConn) Read(p []byte) (int, error) {
	if err := c.Conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	count, err := c.Conn.Read(p)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		err = &awshttp.ResponseTimeoutError{TimeoutDur: c.timeout}
	}
	return count, err
}

// Keep Smithy's HTTP handling, error classification, body cleanup and the SDK's
// serialization, signing, retries and response decoding around Vista egress.
func awsCredentialDispatch(operation context.Context, client smithyhttp.ClientDo) func(*middleware.Stack) error {
	handler := smithyhttp.NewClientHandlerWithOptions(client)
	return func(stack *middleware.Stack) error {
		return stack.Deserialize.Add(middleware.DeserializeMiddlewareFunc("VistaCredentialEgress", func(ctx context.Context, in middleware.DeserializeInput, _ middleware.DeserializeHandler) (middleware.DeserializeOutput, middleware.Metadata, error) {
			response, metadata, err := handler.Handle(ctx, in.Request)
			if cause := operation.Err(); cause != nil {
				if raw, ok := response.(*smithyhttp.Response); ok {
					raw.Body.Close()
				}
				err = &smithy.CanceledError{Err: cause}
			}
			return middleware.DeserializeOutput{RawResponse: response}, metadata, err
		}), middleware.After)
	}
}

func markAWSMetadataRequests(stack *middleware.Stack) error {
	return stack.Initialize.Add(middleware.InitializeMiddlewareFunc("VistaRuntimeIdentity", func(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
		switch in.Parameters.(type) {
		case *imds.GetMetadataInput, *imds.GetRegionInput, *imds.GetDynamicDataInput,
			*imds.GetUserDataInput, *imds.GetIAMInfoInput, *imds.GetInstanceIdentityDocumentInput:
			ctx = context.WithValue(ctx, awsMetadataRequestKey{}, imds.ServiceID)
		}
		return next.HandleInitialize(ctx, in)
	}), middleware.Before)
}
