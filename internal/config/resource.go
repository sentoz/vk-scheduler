package config

import (
	"fmt"
	"regexp"
)

var (
	resourceIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	managedIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	projectIDPattern  = regexp.MustCompile(`^(?:[0-9a-f]{32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
	regionPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// ResourceRef selects an existing resource within the application's cloud scope.
type ResourceRef struct {
	Type string `json:"type" jsonschema:"enum=vm,enum=k8s_v1,enum=k8s_v2"`
	ID   string `json:"id" jsonschema:"pattern=^[A-Za-z0-9][A-Za-z0-9_-]*$"`
}

func (r ResourceRef) withScope(cloud CloudConfig) Resource {
	return Resource{Type: r.Type, ID: r.ID, ProjectID: cloud.ProjectID, Region: cloud.Region}
}

// Resource identifies a runtime resource in the application's project and region.
type Resource struct {
	Type      string `yaml:"type" json:"type"`
	ID        string `yaml:"id" json:"id"`
	ProjectID string `yaml:"project_id" json:"project_id"`
	Region    string `yaml:"region" json:"region"`
}

// Key returns the shared identity used by scheduling, locking, and the UI.
func (r Resource) Key() string {
	return r.ProjectID + ":" + r.Region + ":" + r.Type + ":" + r.ID
}

// Validate checks identity and rejects resources outside the configured scope.
func (r Resource) Validate(projectID, region string) error {
	if r.Type != "vm" && r.Type != "k8s_v1" && r.Type != "k8s_v2" {
		return fmt.Errorf("resource.type: %w", ErrInvalidResourceType)
	}
	if r.Type == "k8s_v2" {
		if !managedIDPattern.MatchString(r.ID) {
			return fmt.Errorf("resource.id for k8s_v2 must start with an ASCII letter or digit and contain only ASCII letters, digits, underscores or hyphens: %w", ErrInvalidResourceID)
		}
	} else if !resourceIDPattern.MatchString(r.ID) {
		return fmt.Errorf("resource.id for %s must be a lowercase UUID: %w", r.Type, ErrInvalidResourceID)
	}
	if !projectIDPattern.MatchString(r.ProjectID) {
		return fmt.Errorf("resource.project_id: %w", ErrInvalidProjectID)
	}
	if !regionPattern.MatchString(r.Region) {
		return fmt.Errorf("resource.region: %w", ErrInvalidRegion)
	}
	if r.ProjectID != projectID || r.Region != region {
		return fmt.Errorf("resource scope: %w", ErrResourceScopeMismatch)
	}
	return nil
}
