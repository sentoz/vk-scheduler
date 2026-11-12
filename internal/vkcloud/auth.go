package vkcloud

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"

	"github.com/sentoz/vk-scheduler/internal/config"
)

// AuthConfig contains credentials kept outside serializable configuration.
type AuthConfig struct {
	Type     string `json:"-" yaml:"-"`
	Username string `json:"-" yaml:"-"`
	Password string `json:"-" yaml:"-"`
	Token    string `json:"-" yaml:"-"`
}

// AuthFromEnv loads credentials for an explicitly selected authentication mode.
func AuthFromEnv(authType string) (AuthConfig, error) {
	auth := AuthConfig{Type: authType}
	switch authType {
	case "v3password":
		auth.Username = os.Getenv("OS_USERNAME")
		auth.Password = os.Getenv("OS_PASSWORD")
		auth.Token = os.Getenv("OS_TOKEN")
	case "v3token":
		auth.Token = os.Getenv("OS_TOKEN")
	}
	if err := auth.Validate(); err != nil {
		return AuthConfig{}, err
	}
	return auth, nil
}

// Validate checks the selected mode without exposing or rewriting credential values.
func (a AuthConfig) Validate() error {
	switch a.Type {
	case "v3password":
		if strings.TrimSpace(a.Username) == "" || a.Password == "" {
			return fmt.Errorf("OS_USERNAME and OS_PASSWORD required: %w", ErrMissingCredentials)
		}
		if a.Token != "" {
			return fmt.Errorf("clear OS_TOKEN or select v3token: %w", ErrMissingCredentials)
		}
	case "v3token":
		if strings.TrimSpace(a.Token) == "" {
			return fmt.Errorf("OS_TOKEN required: %w", ErrMissingCredentials)
		}
	default:
		return fmt.Errorf("unsupported OS_AUTH_TYPE: %w", ErrMissingCredentials)
	}
	return nil
}

func authOptions(cloud config.CloudConfig, auth AuthConfig) gophercloud.AuthOptions {
	options := gophercloud.AuthOptions{
		IdentityEndpoint: cloud.AuthURL,
		Scope:            &gophercloud.AuthScope{ProjectID: cloud.ProjectID},
		AllowReauth:      false,
	}
	if auth.Type == "v3token" {
		options.TokenID = auth.Token
	} else {
		options.Username = auth.Username
		options.Password = auth.Password
		options.DomainName = cloud.UserDomainName
	}
	return options
}

func validateAuthResult(provider *gophercloud.ProviderClient, projectID string, now time.Time) error {
	result, ok := provider.GetAuthResult().(tokens.CreateResult)
	if !ok || provider.Token() == "" {
		return ErrUnexpectedResponse
	}
	project, err := result.ExtractProject()
	if err != nil || project == nil {
		return ErrUnexpectedResponse
	}
	if project.ID != projectID {
		return fmt.Errorf("token project differs from OS_PROJECT_ID: %w", ErrScopeMismatch)
	}
	token, err := result.ExtractToken()
	if err != nil || token == nil || !token.ExpiresAt.After(now) {
		return fmt.Errorf("token is expired or malformed: %w", ErrInvalidCredentials)
	}
	return nil
}

func authenticateProvider(ctx context.Context, provider *gophercloud.ProviderClient,
	cloud config.CloudConfig, auth AuthConfig) error {
	options := authOptions(cloud, auth)
	if err := openstack.AuthenticateV3(ctx, provider, &options, gophercloud.EndpointOpts{}); err != nil {
		return safeAPIError("authenticate", err)
	}
	if err := validateAuthResult(provider, cloud.ProjectID, time.Now()); err != nil {
		return err
	}
	// Reuse the original credential, not the scoped token issued by Keystone.
	provider.ReauthFunc = func(ctx context.Context) error {
		refreshCtx, cancel := context.WithTimeout(ctx, cloud.RequestTimeout.Std())
		defer cancel()
		candidate, err := openstack.NewClient(cloud.AuthURL)
		if err != nil {
			return safeAPIError("prepare reauthentication", err)
		}
		candidate.HTTPClient = provider.HTTPClient
		candidate.UserAgent = provider.UserAgent
		if err := openstack.AuthenticateV3(refreshCtx, candidate, &options, gophercloud.EndpointOpts{}); err != nil {
			return safeAPIError("reauthenticate", err)
		}
		if err := validateAuthResult(candidate, cloud.ProjectID, time.Now()); err != nil {
			return err
		}
		provider.CopyTokenFrom(candidate)
		return nil
	}
	return nil
}
