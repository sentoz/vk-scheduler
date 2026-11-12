package vkcloud

import (
	"net/url"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"
)

func validateServiceURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Opaque != "" || parsed.RawPath != "" || parsed.ForceQuery {
		return "", ErrEndpointNotFound
	}
	return strings.TrimRight(parsed.String(), "/") + "/", nil
}

func catalogEndpoint(catalog *tokens.ServiceCatalog, serviceType, region, iface string) (string, error) {
	if catalog == nil {
		return "", ErrEndpointNotFound
	}
	matches := make([]string, 0, 1)
	for _, service := range catalog.Entries {
		if service.Type != serviceType {
			continue
		}
		for _, endpoint := range service.Endpoints {
			if endpoint.Region == region && endpoint.Interface == iface {
				matches = append(matches, endpoint.URL)
			}
		}
	}
	if len(matches) != 1 {
		return "", ErrEndpointNotFound
	}
	return validateServiceURL(matches[0])
}

func serviceClient(provider *gophercloud.ProviderClient, endpoint, serviceType,
	magnumVersion string) *gophercloud.ServiceClient {
	client := &gophercloud.ServiceClient{
		ProviderClient: provider,
		Endpoint:       endpoint,
		Type:           serviceType,
	}
	if serviceType == "container-infra" {
		client.MoreHeaders = map[string]string{
			"MCS-API-Version": "container-infra " + magnumVersion,
		}
	}
	return client
}
