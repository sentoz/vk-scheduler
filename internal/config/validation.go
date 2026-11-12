package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var microversionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

func validURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.Opaque == "" && u.RawPath == ""
}

// Validate checks effective values without applying zero-value defaults.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Timezone.String()) == "" {
		return fmt.Errorf("timezone is required: %w", ErrInvalidConfig)
	}
	if _, err := c.Location(); err != nil {
		return fmt.Errorf("timezone: %w", ErrInvalidConfig)
	}
	if strings.TrimSpace(c.SchedulesDir) == "" || strings.Contains(c.SchedulesDir, "${") {
		return fmt.Errorf("schedules_dir: %w", ErrInvalidConfig)
	}
	if c.MaxConcurrentJobs < 1 {
		return ErrInvalidConcurrency
	}
	if c.ValidationInterval.Std() <= 0 || c.ShutdownTimeout.Std() <= 0 || c.OperationTimeout.Std() <= 0 {
		return ErrInvalidOperationTimeout
	}
	if c.MetricsPort < 1 || c.MetricsPort > 65535 {
		return fmt.Errorf("metrics_port: %w", ErrInvalidConfig)
	}
	return c.Cloud.Validate(c.OperationTimeout.Std())
}

// Validate checks scope, service URLs, and request budgets before authentication.
func (c CloudConfig) Validate(operationTimeout time.Duration) error {
	if c.MaxConcurrentReads < 1 {
		return ErrInvalidConcurrency
	}
	if operationTimeout <= 0 || c.RequestTimeout.Std() <= 0 || c.PollInterval.Std() <= 0 || c.PollInterval.Std() >= operationTimeout || operationTimeout < c.RequestTimeout.Std() {
		return ErrInvalidOperationTimeout
	}
	if !projectIDPattern.MatchString(c.ProjectID) {
		return ErrInvalidProjectID
	}
	if !regionPattern.MatchString(c.Region) {
		return ErrInvalidRegion
	}
	if !validURL(c.AuthURL) || !strings.HasSuffix(strings.TrimRight(c.AuthURL, "/"), "/v3") {
		return fmt.Errorf("cloud.auth_url must be an HTTPS v3 base: %w", ErrInvalidConfig)
	}
	for _, endpoint := range []string{c.ComputeEndpoint, c.MagnumEndpoint, c.ManagedK8sEndpoint} {
		if endpoint != "" && !validURL(endpoint) {
			return fmt.Errorf("cloud service endpoint: %w", ErrInvalidConfig)
		}
	}
	if strings.TrimSpace(c.UserDomainName) == "" || strings.Contains(c.UserDomainName, "${") {
		return fmt.Errorf("cloud.user_domain_name: %w", ErrInvalidConfig)
	}
	if c.Interface != "public" && c.Interface != "internal" && c.Interface != "admin" {
		return fmt.Errorf("cloud.interface: %w", ErrInvalidConfig)
	}
	if !microversionPattern.MatchString(c.MagnumMicroversion) {
		return fmt.Errorf("cloud.magnum_microversion: %w", ErrInvalidConfig)
	}
	return nil
}
