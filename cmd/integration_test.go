package cmd

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// binPath is the path to the compiled test binary
var binPath string

func TestMain(m *testing.M) {
	// Build the binary once
	fmt.Println("Building surge_integration_test binary...")

	// We build main module (assuming we are at root)
	// But we are in "cmd" package. modifying path.
	rootDir, _ := os.Getwd()
	// If running from root:
	if !strings.HasSuffix(rootDir, "cmd") {
		// running from root
	} else {
		// running from cmd
		rootDir = filepath.Dir(rootDir)
	}

	tempDir := os.TempDir()
	binPath = filepath.Join(tempDir, "surge_integration_test")
	if runtimeOS := "linux"; runtimeOS == "windows" { // simplified check
		binPath += ".exe"
	}

	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	buildCmd.Dir = rootDir // Build from root
	out, err := buildCmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Video build failed: %v\n%s\n", err, out)
		os.Exit(1)
	}

	// Run tests
	exitCode := m.Run()

	// Cleanup
	os.Remove(binPath)
	os.Exit(exitCode)
}

func TestIntegration_SingleInstance(t *testing.T) {
	// Setup isolation
	tempHome := t.TempDir()

	// Create clean env
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "XDG_CONFIG_HOME=") && !strings.HasPrefix(e, "HOME=") {
			env = append(env, e)
		}
	}
	// Set XDG_CONFIG_HOME for Linux/Mac isolation
	env = append(env, fmt.Sprintf("XDG_CONFIG_HOME=%s", tempHome))
	// Also Mock HOME just in case
	env = append(env, fmt.Sprintf("HOME=%s", tempHome))

	// Create dummy file for download
	dummyFile := filepath.Join(tempHome, "test.bin")
	f, _ := os.Create(dummyFile)
	f.WriteString("dummy content")
	f.Close()

	// Start Mock HTTP Server
	// We can't easily spawn a simple python server without dependencies,
	// so let's start a small go http server in a goroutine?
	// No, we need it to be accessible by the subprocess.
	// We can use the test process itself as the HTTP server.

	serverMux := http.NewServeMux()
	serverMux.HandleFunc("/file", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("dummy content"))
	})
	server := &http.Server{Addr: "127.0.0.1:0", Handler: serverMux}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	go server.Serve(ln)
	defer server.Close()

	fileUrl := fmt.Sprintf("http://127.0.0.1:%d/file", port)

	// --- Step 1: Start Host ---
	// Output dir
	outDir := filepath.Join(tempHome, "downloads")

	// Start Headless Host
	cmdHost := exec.Command(binPath, "get", fileUrl, "--output", outDir)
	cmdHost.Env = env
	var hostOut, hostErr bytes.Buffer
	cmdHost.Stdout = &hostOut
	cmdHost.Stderr = &hostErr

	// Start in background
	err = cmdHost.Start()
	require.NoError(t, err)

	defer func() {
		if cmdHost.Process != nil {
			cmdHost.Process.Kill()
		}
	}()

	// Wait for host to start (check for "Headless Host" string in output?)
	// But getCmd headless host output is redirected?
	// No, root.go prints to stdout/stderr.

	// We wait a bit
	time.Sleep(2 * time.Second)

	// Check if process is still running
	if cmdHost.ProcessState != nil && cmdHost.ProcessState.Exited() {
		t.Fatalf("Host process unexpectedly finished. Out: %s, Err: %s", hostOut.String(), hostErr.String())
	}

	// Verify Lock File
	lockPath := filepath.Join(tempHome, "surge", "surge.lock")
	_, err = os.Stat(lockPath)
	require.NoError(t, err, "Lock file should exist")

	// --- Step 2: Start Client (Offload) ---
	cmdClient := exec.Command(binPath, "get", fileUrl, "--output", outDir)
	cmdClient.Env = env
	var clientOut, clientErr bytes.Buffer
	cmdClient.Stdout = &clientOut
	cmdClient.Stderr = &clientErr

	err = cmdClient.Run()
	require.NoError(t, err, "Client should exit successfully (offloaded)")

	// Verify Client Output
	output := clientOut.String() + clientErr.String()
	// "Download queued" happens in stdout usually?
	// Based on get.go: fmt.Printf("Download queued: %s\n", string(body))
	// Based on root.go: fmt.Printf("Downloading: %s ...") (Host output)

	if !strings.Contains(output, "Download queued") {
		t.Errorf("Client did not report queued download. Output: %s", output)
	}

	// --- Step 3: Verify Host Logic ---
	// Host should eventually exit when downloads are done.
	// We gave it 2 downloads of small file (dummy content).

	// Wait for host process
	done := make(chan error, 1)
	go func() {
		done <- cmdHost.Wait()
	}()

	select {
	case err := <-done:
		require.NoError(t, err, "Host should exit cleanly")
	case <-time.After(5 * time.Second):
		t.Fatal("Host timed out waiting for completion")
	}

	// Verify output
	hostLog := hostOut.String() + hostErr.String()
	require.Contains(t, hostLog, "Headless Host", "Host execution verified")
}
