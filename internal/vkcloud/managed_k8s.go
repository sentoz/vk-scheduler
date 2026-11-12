package vkcloud

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/sentoz/vk-scheduler/internal/config"
)

type managedClusterView struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Status    string `json:"status"`
}

func (c *Client) getManagedK8sState(ctx context.Context, target config.Resource) (string, bool, error) {
	service := c.service("k8s_v2")
	if service == nil {
		return "", false, ErrEndpointNotFound
	}
	var cluster managedClusterView
	err := readWithRetry(ctx, func(parent context.Context) error {
		requestCtx, cancel := context.WithTimeout(parent, c.cloud.RequestTimeout.Std())
		defer cancel()
		cluster = managedClusterView{}
		_, err := service.Get(requestCtx, service.ServiceURL("clusters", target.ID),
			&cluster, &gophercloud.RequestOpts{OkCodes: []int{200}})
		return err
	}, waitContext)
	if err != nil {
		return "", false, safeAPIError("get managed Kubernetes cluster", err)
	}
	if cluster.ID != target.ID || cluster.ProjectID == "" {
		return "", false, ErrUnexpectedResponse
	}
	if cluster.ProjectID != target.ProjectID {
		return "", false, ErrScopeMismatch
	}
	return managedK8sState(cluster.Status)
}

func (c *Client) postManagedK8sAction(ctx context.Context, target config.Resource, desired string) error {
	action := "turn_off"
	if desired == "running" {
		action = "turn_on"
	}
	service := c.service("k8s_v2")
	// The API acknowledges the command; only a later GET can confirm completion.
	_, err := service.Post(ctx, service.ServiceURL("clusters", target.ID, action),
		struct{}{}, nil, &gophercloud.RequestOpts{OkCodes: []int{200, 202, 204}})
	return safeAPIError("switch managed Kubernetes cluster state", err)
}
