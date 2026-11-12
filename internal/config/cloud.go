package config

// CloudConfig configures the VK Cloud API scope and request behavior.
type CloudConfig struct {
	AuthURL            string   `yaml:"auth_url" json:"auth_url"`
	ProjectID          string   `yaml:"project_id,omitempty" json:"project_id,omitempty"`
	Region             string   `yaml:"region,omitempty" json:"region,omitempty"`
	UserDomainName     string   `yaml:"user_domain_name" json:"user_domain_name"`
	Interface          string   `yaml:"interface" json:"interface"`
	MaxConcurrentReads int      `yaml:"max_concurrent_reads" json:"max_concurrent_reads"`
	ComputeEndpoint    string   `yaml:"compute_endpoint,omitempty" json:"compute_endpoint,omitempty"`
	MagnumEndpoint     string   `yaml:"magnum_endpoint,omitempty" json:"magnum_endpoint,omitempty"`
	ManagedK8sEndpoint string   `yaml:"managed_k8s_endpoint,omitempty" json:"managed_k8s_endpoint,omitempty"`
	MagnumMicroversion string   `yaml:"magnum_microversion" json:"magnum_microversion"`
	RequestTimeout     Duration `yaml:"request_timeout" json:"request_timeout"`
	PollInterval       Duration `yaml:"poll_interval" json:"poll_interval"`
}

func setDefault(document map[string]any, key string, value any) {
	if _, exists := document[key]; !exists {
		document[key] = value
	}
}
