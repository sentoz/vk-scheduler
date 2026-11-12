package vkcloud

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"

	"github.com/sentoz/vk-scheduler/internal/config"
	"github.com/sentoz/vk-scheduler/internal/resource"
)

type clusterView struct {
	UUID      string `json:"uuid"`
	ProjectID string `json:"project_id"`
	Status    string `json:"status"`
	NewStatus string `json:"new_status"`
}

// GetState returns a stable state or a transition marker after validating scope.
func (c *Client) GetState(ctx context.Context, target config.Resource) (string, bool, error) {
	if target.Type != "vm" && target.Type != "k8s_v1" && target.Type != "k8s_v2" {
		return "", false, resource.ErrUnsupportedResourceType
	}
	if err := target.Validate(c.cloud.ProjectID, c.cloud.Region); err != nil {
		return "", false, err
	}
	switch target.Type {
	case "vm":
		return c.getVMState(ctx, target)
	case "k8s_v1":
		return c.getClusterState(ctx, target)
	case "k8s_v2":
		return c.getManagedK8sState(ctx, target)
	default:
		return "", false, ErrUnexpectedResponse
	}
}

func (c *Client) getVMState(ctx context.Context, target config.Resource) (string, bool, error) {
	compute := c.service("vm")
	if compute == nil {
		return "", false, ErrEndpointNotFound
	}
	var server *servers.Server
	err := readWithRetry(ctx, func(parent context.Context) error {
		requestCtx, cancel := context.WithTimeout(parent, c.cloud.RequestTimeout.Std())
		defer cancel()
		var requestErr error
		server, requestErr = servers.Get(requestCtx, compute, target.ID).Extract()
		return requestErr
	}, waitContext)
	if err != nil {
		return "", false, safeAPIError("get instance", err)
	}
	if server == nil || server.ID != target.ID || server.TenantID == "" {
		return "", false, ErrUnexpectedResponse
	}
	if server.TenantID != target.ProjectID {
		return "", false, ErrScopeMismatch
	}
	return novaState(server.Status, server.TaskState)
}

func (c *Client) getClusterState(ctx context.Context, target config.Resource) (string, bool, error) {
	magnum := c.service("k8s_v1")
	if magnum == nil {
		return "", false, ErrEndpointNotFound
	}
	var cluster clusterView
	err := readWithRetry(ctx, func(parent context.Context) error {
		requestCtx, cancel := context.WithTimeout(parent, c.cloud.RequestTimeout.Std())
		defer cancel()
		cluster = clusterView{}
		_, requestErr := magnum.Get(requestCtx, magnum.ServiceURL("clusters", target.ID),
			&cluster, &gophercloud.RequestOpts{OkCodes: []int{200}})
		return requestErr
	}, waitContext)
	if err != nil {
		return "", false, safeAPIError("get cluster", err)
	}
	if cluster.UUID != target.ID || cluster.ProjectID == "" {
		return "", false, ErrUnexpectedResponse
	}
	if cluster.ProjectID != target.ProjectID {
		return "", false, ErrScopeMismatch
	}
	return magnumState(cluster.Status, cluster.NewStatus)
}
