package vkcloud

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"

	"github.com/sentoz/vk-scheduler/internal/config"
	"github.com/sentoz/vk-scheduler/internal/vars"
)

// Client manages existing Nova instances, Magnum clusters, and Managed-k8s clusters.
type Client struct {
	services         sync.RWMutex
	activeMu         sync.Mutex
	active           map[string]bool
	provider         *gophercloud.ProviderClient
	compute          *gophercloud.ServiceClient
	magnum           *gophercloud.ServiceClient
	managedK8s       *gophercloud.ServiceClient
	cloud            config.CloudConfig
	operationTimeout time.Duration
	httpClient       *http.Client
	operations       pendingOperations
	readOnce         sync.Once
	readSlots        chan struct{}
	closeOnce        sync.Once
}

// NewClient authenticates and prepares only the services required by resources.
func NewClient(ctx context.Context, cloud config.CloudConfig, auth AuthConfig,
	resources []config.Resource, operationTimeout time.Duration) (*Client, error) {
	if err := cloud.Validate(operationTimeout); err != nil {
		return nil, fmt.Errorf("cloud config: %w", err)
	}
	if err := auth.Validate(); err != nil {
		return nil, err
	}
	for _, target := range resources {
		if err := target.Validate(cloud.ProjectID, cloud.Region); err != nil {
			return nil, fmt.Errorf("client resource: %w", err)
		}
	}
	provider, err := openstack.NewClient(cloud.AuthURL)
	if err != nil {
		return nil, safeAPIError("prepare identity", err)
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: cloud.RequestTimeout.Std(),
		ExpectContinueTimeout: time.Second,
	}
	client := &Client{provider: provider, cloud: cloud, operationTimeout: operationTimeout}
	provider.HTTPClient = http.Client{
		Transport: &readTransport{base: transport, client: client},
		Timeout:   cloud.RequestTimeout.Std(),
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return ErrUnexpectedResponse
		},
	}
	// Keep SDK network and rate-limit retries disabled; GET retries live below.
	provider.RetryFunc = nil
	provider.RetryBackoffFunc = nil
	provider.UserAgent.Prepend("vk-scheduler/" + vars.Version)
	success := false
	defer func() {
		if !success {
			transport.CloseIdleConnections()
		}
	}()

	authCtx, cancel := context.WithTimeout(ctx, cloud.RequestTimeout.Std())
	defer cancel()
	if err := authenticateProvider(authCtx, provider, cloud, auth); err != nil {
		return nil, err
	}
	client.httpClient = &provider.HTTPClient
	if err := client.PrepareServices(resources); err != nil {
		return nil, err
	}
	success = true
	return client, nil
}

// Shutdown closes idle connections after active operations have drained.
func (c *Client) Shutdown(_ context.Context) error {
	c.closeOnce.Do(func() {
		if c.httpClient != nil {
			c.httpClient.CloseIdleConnections()
		}
	})
	return nil
}

// ValidateCredentials verifies the current token's project and expiry.
func (c *Client) ValidateCredentials(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return validateAuthResult(c.provider, c.cloud.ProjectID, time.Now())
}

// PrepareServices resolves newly needed services before publishing a reload.
func (c *Client) PrepareServices(resources []config.Resource) error {
	c.services.RLock()
	compute, magnum, managed := c.compute, c.magnum, c.managedK8s
	c.services.RUnlock()
	var catalog *tokens.ServiceCatalog
	for _, target := range resources {
		if err := target.Validate(c.cloud.ProjectID, c.cloud.Region); err != nil {
			return err
		}
		var destination **gophercloud.ServiceClient
		serviceType, override := "compute", c.cloud.ComputeEndpoint
		switch target.Type {
		case "vm":
			destination = &compute
		case "k8s_v1":
			destination = &magnum
			serviceType = "container-infra"
			override = c.cloud.MagnumEndpoint
		case "k8s_v2":
			destination = &managed
			serviceType = "managed-k8s"
			override = c.cloud.ManagedK8sEndpoint
		}
		if *destination != nil {
			continue
		}
		endpoint := override
		var err error
		if endpoint == "" {
			if catalog == nil {
				result, ok := c.provider.GetAuthResult().(tokens.CreateResult)
				if !ok {
					return ErrUnexpectedResponse
				}
				catalog, err = result.ExtractServiceCatalog()
				if err != nil {
					return safeAPIError("read service catalog", err)
				}
			}
			endpoint, err = catalogEndpoint(catalog, serviceType, c.cloud.Region, c.cloud.Interface)
		} else {
			endpoint, err = validateServiceURL(endpoint)
		}
		if err != nil {
			return fmt.Errorf("resolve %s endpoint in region %s (%s): %w", serviceType, c.cloud.Region, c.cloud.Interface, err)
		}
		*destination = serviceClient(c.provider, endpoint, serviceType, c.cloud.MagnumMicroversion)
	}
	c.services.Lock()
	if c.compute == nil {
		c.compute = compute
	}
	if c.magnum == nil {
		c.magnum = magnum
	}
	if c.managedK8s == nil {
		c.managedK8s = managed
	}
	c.services.Unlock()
	return nil
}

func (c *Client) service(kind string) *gophercloud.ServiceClient {
	c.services.RLock()
	defer c.services.RUnlock()
	switch kind {
	case "vm":
		return c.compute
	case "k8s_v1":
		return c.magnum
	case "k8s_v2":
		return c.managedK8s
	default:
		return nil
	}
}

func (c *Client) claim(key string) bool {
	c.activeMu.Lock()
	defer c.activeMu.Unlock()
	if c.active[key] {
		return false
	}
	if c.active == nil {
		c.active = map[string]bool{}
	}
	c.active[key] = true
	return true
}

func (c *Client) release(key string) { c.activeMu.Lock(); delete(c.active, key); c.activeMu.Unlock() }

// PendingOperations returns a copy of uncertain attempts retained in this process.
func (c *Client) PendingOperations() []PendingOperation { return c.operations.Records() }

// Unconfirmed reports a process-local operation awaiting stable confirmation.
func (c *Client) Unconfirmed(key string) bool { _, exists := c.operations.Pending(key); return exists }
