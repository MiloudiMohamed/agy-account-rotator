package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/MiloudiMohamed/agy-account-rotator/internal/store"
)

// IsRunning queries the local proxy healthz endpoint.
func IsRunning(port int) bool {
	if port <= 0 {
		port = 8999
	}
	client := &http.Client{Timeout: 35 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// FetchHealth returns current proxy runtime diagnostics if running.
func FetchHealth(port int) (*HealthResponse, error) {
	if port <= 0 {
		port = 8999
	}
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("proxy returned status %d", resp.StatusCode)
	}
	var hr HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&hr); err != nil {
		return nil, err
	}
	return &hr, nil
}

// EnsureRunning guarantees the local proxy is running, spawning it in the background if necessary.
func EnsureRunning(s *store.Store) error {
	cfg := s.LoadConfig()
	if !cfg.ProxyEnabled {
		return nil
	}

	port := cfg.ProxyPort
	if port <= 0 {
		port = 8999
	}

	// Fast path: already running
	if IsRunning(port) {
		return nil
	}

	// Ensure CA certificate is created beforehand
	if _, err := LoadOrCreateCA(s); err != nil {
		return fmt.Errorf("ensuring CA certificate: %w", err)
	}

	// Locate agy-rotator binary
	binPath, err := os.Executable()
	if err != nil {
		binPath = "agy-rotator"
	}
	if lp, lerr := exec.LookPath(binPath); lerr == nil {
		binPath = lp
	}

	// Open or create log file for proxy daemon
	logFile, err := os.OpenFile(s.ProxyLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		logFile = nil
	}

	cmd := exec.Command(binPath, "proxy", "daemon")
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	// Detach process group so it survives parent exit
	setDetachAttr(cmd)

	if err := cmd.Start(); err != nil {
		if logFile != nil {
			logFile.Close()
		}
		return fmt.Errorf("spawning proxy daemon: %w", err)
	}

	if logFile != nil {
		_ = logFile.Close()
	}

	// Wait up to 500ms for healthz to become ready
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if IsRunning(port) {
			return nil
		}
		time.Sleep(15 * time.Millisecond)
	}

	return nil
}

// Stop sends a shutdown signal to the running proxy and cleans up.
func Stop(s *store.Store) error {
	cfg := s.LoadConfig()
	port := cfg.ProxyPort
	if port <= 0 {
		port = 8999
	}

	// Try HTTP shutdown endpoint first
	client := &http.Client{Timeout: 500 * time.Millisecond}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/shutdown", port), nil)
	_, _ = client.Do(req)

	// Also check PID file as fallback
	pidPath := s.ProxyPIDPath()
	if data, err := os.ReadFile(pidPath); err == nil {
		pidStr := strings.TrimSpace(string(data))
		if pid, perr := strconv.Atoi(pidStr); perr == nil && pid > 0 {
			if proc, ferr := os.FindProcess(pid); ferr == nil {
				terminateProcess(proc)
			}
		}
		_ = os.Remove(pidPath)
	}

	return nil
}

// BinaryEnv returns proxy environment variables for child processes (agy).
func BinaryEnv(s *store.Store) []string {
	cfg := s.LoadConfig()
	if !cfg.ProxyEnabled {
		return nil
	}
	port := cfg.ProxyPort
	if port <= 0 {
		port = 8999
	}
	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	caPath := s.CAPath()

	return []string{
		"HTTP_PROXY=" + proxyURL,
		"HTTPS_PROXY=" + proxyURL,
		"ALL_PROXY=" + proxyURL,
		"SSL_CERT_FILE=" + caPath,
		"NODE_EXTRA_CA_CERTS=" + caPath,
		"GRPC_DEFAULT_SSL_ROOTS_FILE_PATH=" + caPath,
	}
}
