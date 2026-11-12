package vkcloud

import (
	"context"
	"errors"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"

	"github.com/sentoz/vk-scheduler/internal/config"
)

// ObservePending confirms only a previously recorded stable target.
func (c *Client) ObservePending(ctx context.Context, target config.Resource) (bool, error) {
	if !c.claim(target.Key()) {
		return false, ErrOperationConflict
	}
	defer c.release(target.Key())
	return c.observePending(ctx, target)
}

func (c *Client) observePending(ctx context.Context, target config.Resource) (bool, error) {
	record, exists := c.operations.Pending(target.Key())
	if !exists {
		return true, nil
	}
	state, transitional, err := c.GetState(ctx, record.Resource)
	if err != nil {
		return false, err
	}
	if transitional || state != record.Desired {
		return false, ErrUnconfirmedOperation
	}
	return true, c.operations.Confirm(record)
}

// Apply authorizes one mutation and waits only for stable cloud resource state.
func (c *Client) Apply(parent context.Context, target config.Resource, action string,
	authorize func(context.Context) error) (bool, error) {
	if !c.claim(target.Key()) {
		return false, ErrOperationConflict
	}
	defer c.release(target.Key())
	if authorize == nil || (action != "start" && action != "stop") {
		return false, ErrUnexpectedResponse
	}
	ctx, cancel := context.WithTimeout(parent, c.operationTimeout)
	defer cancel()
	changed, err := c.apply(ctx, target, action, authorize)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("resource operation: %w", errors.Join(err, ErrOperationTimeout, context.DeadlineExceeded))
	}
	return changed, err
}

func (c *Client) apply(ctx context.Context, target config.Resource, action string,
	authorize func(context.Context) error) (bool, error) {
	if ok, err := c.observePending(ctx, target); err != nil || !ok {
		return false, err
	}
	desired := "running"
	if action == "stop" {
		desired = "stopped"
	}
	for {
		state, transitional, err := c.GetState(ctx, target)
		if err != nil {
			return false, err
		}
		if transitional {
			if err := waitContext(ctx, c.cloud.PollInterval.Std()); err != nil {
				return false, err
			}
			continue
		}
		if state == desired {
			return false, nil
		}
		record, err := c.operations.Begin(target, desired)
		if err != nil {
			return false, err
		}
		if err := authorize(ctx); err != nil {
			if cleanupErr := c.operations.Confirm(record); cleanupErr != nil {
				return false, cleanupErr
			}
			return false, err
		}
		if err := c.postAction(ctx, target, desired); err != nil {
			if rejectedAction(err) {
				cleanup := c.operations.Confirm(record)
				if errors.Is(err, ErrOperationConflict) {
					_, _, diagnostic := c.GetState(ctx, target)
					return true, errors.Join(err, diagnostic, cleanup)
				}
				return true, errors.Join(err, cleanup)
			}
			return true, errors.Join(ErrUnconfirmedOperation, err)
		}
		for {
			state, transitional, err := c.GetState(ctx, target)
			if err != nil {
				return true, errors.Join(ErrUnconfirmedOperation, err)
			}
			if !transitional && state == desired {
				return true, c.operations.Confirm(record)
			}
			if err := waitContext(ctx, c.cloud.PollInterval.Std()); err != nil {
				return true, errors.Join(ErrUnconfirmedOperation, err)
			}
		}
	}
}

func rejectedAction(err error) bool {
	var response gophercloud.ErrUnexpectedResponseCode
	if !errors.As(err, &response) {
		return false
	}
	switch response.Actual {
	case 400, 401, 403, 404, 409, 429:
		return true
	default:
		return false
	}
}

func (c *Client) postAction(ctx context.Context, target config.Resource, desired string) error {
	requestCtx, cancel := context.WithTimeout(ctx, c.cloud.RequestTimeout.Std())
	defer cancel()
	if target.Type == "vm" {
		compute := c.service("vm")
		if desired == "running" {
			return safeAPIError("start instance", servers.Start(requestCtx, compute, target.ID).ExtractErr())
		}
		return safeAPIError("stop instance", servers.Stop(requestCtx, compute, target.ID).ExtractErr())
	}
	if target.Type == "k8s_v2" {
		return c.postManagedK8sAction(requestCtx, target, desired)
	}
	action := "turn_off_cluster"
	if desired == "running" {
		action = "turn_on_cluster"
	}
	body := struct {
		Action  string         `json:"action"`
		Payload map[string]any `json:"payload"`
	}{Action: action, Payload: map[string]any{}}
	var response struct {
		UUID string `json:"uuid"`
	}
	magnum := c.service("k8s_v1")
	_, err := magnum.Post(requestCtx, magnum.ServiceURL("clusters", target.ID, "actions")+"/",
		body, &response, &gophercloud.RequestOpts{OkCodes: []int{202}})
	if err != nil {
		return safeAPIError("switch cluster state", err)
	}
	if response.UUID != target.ID {
		return ErrUnexpectedResponse
	}
	return nil
}
