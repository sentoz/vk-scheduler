package main

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/sentoz/vk-scheduler/internal/schedule"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestCLIProcess(t *testing.T) {
	if os.Getenv("VK_SCHEDULER_TEST_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			if err := run(os.Args[i+1:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	t.Fatal("missing subprocess arguments")
}

func TestImpossibleCronIsRejectedBeforeAuthentication(t *testing.T) {
	var connections atomic.Int32
	api := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid calendar reached auth") }))
	api.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	api.StartTLS()
	defer api.Close()
	directory := t.TempDir()
	schedules := filepath.Join(directory, "schedules")
	if err := os.Mkdir(schedules, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := "apiVersion: scheduler.vk/v1alpha1\nkind: Schedule\nmetadata:\n  name: impossible\nspec:\n  type: cron\n  resource:\n    type: vm\n    id: 11111111-1111-4111-8111-111111111111\n  actions:\n    start:\n      enabled: true\n      crontab: '0 0 31 2 *'\n"
	if err := os.WriteFile(filepath.Join(schedules, "impossible.yaml"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(configPath, []byte("timezone: UTC\nschedules_dir: ./schedules\ncloud: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"OS_AUTH_TYPE": "v3password", "OS_USERNAME": "user", "OS_PASSWORD": "synthetic-password", "OS_TOKEN": "", "OS_AUTH_URL": api.URL + "/infra/identity/v3/", "OS_PROJECT_ID": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "OS_REGION_NAME": "test-region", "OS_USER_DOMAIN_NAME": "users", "OS_INTERFACE": "public", "LOG_LEVEL": "info", "LOG_FORMAT": "json"} {
		t.Setenv(key, value)
	}
	previous, level := log.Logger, zerolog.GlobalLevel()
	defer func() { log.Logger = previous; zerolog.SetGlobalLevel(level) }()
	if err := run([]string{"--config", configPath}); !errors.Is(err, schedule.ErrNoNextOccurrence) {
		t.Fatal("impossible calendar was not rejected early", err)
	}
	if connections.Load() != 0 {
		t.Fatal("validation contacted cloud before rejecting timer")
	}
}

func TestHelpAndVersionDoNotValidateRuntimeEnvironment(t *testing.T) {
	t.Setenv("OS_AUTH_TYPE", "invalid-test-mode")
	t.Setenv("LOG_LEVEL", "invalid-test-level")
	t.Setenv("LOG_FORMAT", "invalid-test-format")
	t.Setenv("VK_SCHEDULER_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	for _, option := range []string{"--help", "--version"} {
		if err := run([]string{option}); err != nil {
			t.Fatalf("%s requires runtime setup: %v", option, err)
		}
	}
}

func TestCLISIGTERMStopsAuthenticatedProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX SIGTERM subprocess test")
	}
	const project = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/infra/identity/v3/auth/tokens" {
			t.Error("unexpected API request")
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Subject-Token", "synthetic-token")
		w.WriteHeader(201)
		if err := json.NewEncoder(w).Encode(map[string]any{"token": map[string]any{"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "project": map[string]string{"id": project}, "catalog": []any{}}}); err != nil {
			t.Error(err)
		}
	}))
	defer api.Close()
	directory := t.TempDir()
	ca := filepath.Join(directory, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "schedules"), 0700); err != nil {
		t.Fatal(err)
	}
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reservation.Addr().(*net.TCPAddr).Port
	configPath := filepath.Join(directory, "config.yaml")
	configText := fmt.Sprintf("timezone: UTC\nschedules_dir: ./schedules\nmetrics_port: %d\nshutdown_timeout: 2s\ncloud:\n  project_id: %s\n  region: test-region\n  auth_url: %s/infra/identity/v3/\n", port, project, api.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	process := startCLIProcess(t, configPath, api.URL+"/infra/identity/v3/", ca)
	process.waitReady(t, port)
	process.stop(t)
}

type cliProcess struct {
	command  *exec.Cmd
	output   bytes.Buffer
	finished chan struct{}
	err      error
}

func startCLIProcess(t *testing.T, configPath, authURL, ca string) *cliProcess {
	t.Helper()
	const project = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestCLIProcess$", "--", "--config", configPath)
	command.Env = []string{"VK_SCHEDULER_TEST_PROCESS=1", "OS_AUTH_TYPE=v3password", "OS_USERNAME=test-user", "OS_PASSWORD=synthetic-password", "OS_AUTH_URL=" + authURL, "OS_PROJECT_ID=" + project, "OS_REGION_NAME=test-region", "OS_USER_DOMAIN_NAME=users", "OS_INTERFACE=public", "SSL_CERT_FILE=" + ca, "LOG_FORMAT=json"}
	p := &cliProcess{command: command, finished: make(chan struct{})}
	command.Stdout = &p.output
	command.Stderr = &p.output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.err = command.Wait(); close(p.finished) }()
	t.Cleanup(func() {
		select {
		case <-p.finished:
			return
		default:
		}
		if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Error(err)
		}
		select {
		case <-p.finished:
		case <-time.After(5 * time.Second):
			t.Error("subprocess did not terminate")
		}
	})
	return p
}

func (p *cliProcess) waitReady(t *testing.T, port int) {
	t.Helper()
	client := &http.Client{Timeout: 200 * time.Millisecond}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	address := fmt.Sprintf("http://127.0.0.1:%d/health/ready", port)
	ready := false
	for !ready {
		select {
		case <-p.finished:
			t.Fatalf("CLI exited during startup: %v\n%s", p.err, p.output.String())
		case <-deadline.C:
			t.Fatal("CLI readiness timed out")
		case <-ticker.C:
			response, err := client.Get(address)
			if err != nil {
				continue
			}
			ready = response.StatusCode == http.StatusOK
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func (p *cliProcess) stop(t *testing.T) {
	t.Helper()
	if err := p.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.finished:
		if p.err != nil {
			t.Fatalf("SIGTERM exit: %v\n%s", p.err, p.output.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CLI ignored SIGTERM")
	}
}
