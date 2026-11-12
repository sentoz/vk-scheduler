package config

// Config contains restart-scoped settings; schedules are loaded separately.
type Config struct {
	Timezone            Timezone    `json:"timezone"`
	SchedulesDir        string      `json:"schedules_dir" jsonschema:"minLength=1"`
	ValidationResources bool        `json:"validation_resources"`
	ValidationInterval  Duration    `json:"validation_interval"`
	ShutdownTimeout     Duration    `json:"shutdown_timeout"`
	OperationTimeout    Duration    `json:"operation_timeout"`
	MaxConcurrentJobs   int         `json:"max_concurrent_jobs" jsonschema:"minimum=1"`
	MetricsEnabled      bool        `json:"metrics_enabled"`
	UIEnabled           bool        `json:"ui_enabled"`
	MetricsPort         int         `json:"metrics_port" jsonschema:"minimum=1,maximum=65535"`
	Cloud               CloudConfig `json:"cloud"`
}

// Schedule is the validated runtime form of one manifest.
type Schedule struct {
	Name           string
	DisplayName    string
	Type           string
	Resource       Resource
	Actions        Actions
	Suspend        bool
	sourceFile     string
	sourceDocument int
}

// ScheduleManifest is a versioned schedule document.
type ScheduleManifest struct {
	APIVersion string               `json:"apiVersion" jsonschema:"enum=scheduler.vk/v1alpha1"`
	Kind       string               `json:"kind" jsonschema:"enum=Schedule"`
	Metadata   ScheduleManifestMeta `json:"metadata"`
	Spec       ScheduleManifestSpec `json:"spec"`
}

// ScheduleManifestMeta holds a unique name and optional display annotations.
type ScheduleManifestMeta struct {
	Name        string            `json:"name" jsonschema:"minLength=1"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ScheduleManifestSpec defines the resource and its calendar actions.
type ScheduleManifestSpec struct {
	Type     string      `json:"type" jsonschema:"enum=daily,enum=weekly,enum=monthly,enum=cron"`
	Resource ResourceRef `json:"resource"`
	Actions  Actions     `json:"actions"`
	Suspend  bool        `json:"suspend,omitempty"`
}

// Actions contains optional start and stop definitions.
type Actions struct {
	Start *ActionConfig `json:"start,omitempty"`
	Stop  *ActionConfig `json:"stop,omitempty"`
}

// ActionConfig describes one enabled or disabled calendar event.
type ActionConfig struct {
	Enabled bool   `json:"enabled"`
	Time    string `json:"time,omitempty"`
	Crontab string `json:"crontab,omitempty"`
	Day     *int   `json:"day,omitempty"`
}
