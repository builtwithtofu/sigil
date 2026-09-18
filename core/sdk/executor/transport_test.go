package executor

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLocalTransportExec_SimpleCommand tests basic command execution
func TestLocalTransportExec_SimpleCommand(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	var stdout, stderr bytes.Buffer
	opts := ExecOpts{
		Stdout: &stdout,
		Stderr: &stderr,
	}

	exitCode, err := transport.Exec(context.Background(), []string{"echo", "hello"}, opts)

	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
	assert.Equal(t, "hello\n", stdout.String())
	assert.Empty(t, stderr.String())
}

// TestLocalTransportExec_WithStdin tests command with stdin
func TestLocalTransportExec_WithStdin(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	var stdout bytes.Buffer
	stdin := strings.NewReader("test input\n")
	opts := ExecOpts{
		Stdin:  stdin,
		Stdout: &stdout,
	}

	exitCode, err := transport.Exec(context.Background(), []string{"cat"}, opts)

	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
	assert.Equal(t, "test input\n", stdout.String())
}

// TestLocalTransportExec_WithEnvironment tests command with custom environment
func TestLocalTransportExec_WithEnvironment(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	var stdout bytes.Buffer
	opts := ExecOpts{
		Stdout: &stdout,
		Env: map[string]string{
			"TEST_VAR": "test_value",
		},
	}

	exitCode, err := transport.Exec(context.Background(), []string{"sh", "-c", "echo $TEST_VAR"}, opts)

	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
	assert.Equal(t, "test_value\n", stdout.String())
}

// TestLocalTransportExec_WithWorkdir tests command with custom working directory
func TestLocalTransportExec_WithWorkdir(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	// Create temp directory
	tmpDir := t.TempDir()

	var stdout bytes.Buffer
	opts := ExecOpts{
		Stdout: &stdout,
		Dir:    tmpDir,
	}

	exitCode, err := transport.Exec(context.Background(), []string{"pwd"}, opts)

	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
	// pwd output should match tmpDir (with newline)
	assert.Equal(t, tmpDir+"\n", stdout.String())
}

// TestLocalTransportExec_NonZeroExit tests command that fails
func TestLocalTransportExec_NonZeroExit(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	var stdout, stderr bytes.Buffer
	opts := ExecOpts{
		Stdout: &stdout,
		Stderr: &stderr,
	}

	exitCode, err := transport.Exec(context.Background(), []string{"sh", "-c", "exit 42"}, opts)

	require.NoError(t, err) // No error, just non-zero exit
	assert.Equal(t, 42, exitCode)
}

// TestLocalTransportExec_CommandNotFound tests command that doesn't exist
func TestLocalTransportExec_CommandNotFound(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	var stdout, stderr bytes.Buffer
	opts := ExecOpts{
		Stdout: &stdout,
		Stderr: &stderr,
	}

	exitCode, err := transport.Exec(context.Background(), []string{"nonexistent-command-xyz"}, opts)

	assert.Error(t, err)
	assert.Equal(t, 127, exitCode) // Convention: 127 for command not found
}

// TestLocalTransportExec_PermissionDenied tests executing a non-executable file
func TestLocalTransportExec_PermissionDenied(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	// Create a temp file without execute permissions
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "script.sh")
	err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho hello\n"), 0o644) // No execute bit
	require.NoError(t, err)

	var stdout, stderr bytes.Buffer
	opts := ExecOpts{
		Stdout: &stdout,
		Stderr: &stderr,
	}

	exitCode, err := transport.Exec(context.Background(), []string{scriptPath}, opts)

	assert.Error(t, err)
	assert.Equal(t, 126, exitCode) // Convention: 126 for permission denied
}

// TestLocalTransportExec_InvalidWorkdir tests command with non-existent working directory
func TestLocalTransportExec_InvalidWorkdir(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	var stdout, stderr bytes.Buffer
	opts := ExecOpts{
		Stdout: &stdout,
		Stderr: &stderr,
		Dir:    "/nonexistent/directory/path",
	}

	exitCode, err := transport.Exec(context.Background(), []string{"echo", "hello"}, opts)

	assert.Error(t, err)
	assert.Equal(t, 1, exitCode) // Convention: 1 for general command failure
}

// TestLocalTransportExec_ExplicitPathNotFound tests executing a non-existent explicit path
func TestLocalTransportExec_ExplicitPathNotFound(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	var stdout, stderr bytes.Buffer
	opts := ExecOpts{
		Stdout: &stdout,
		Stderr: &stderr,
	}

	exitCode, err := transport.Exec(context.Background(), []string{"/tmp/missing/script"}, opts)

	assert.Error(t, err)
	assert.Equal(t, 127, exitCode) // Convention: 127 for command not found (explicit path)
}

// TestLocalTransportExec_ContextCancellation tests context cancellation
func TestLocalTransportExec_ContextCancellation(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	var stdout bytes.Buffer
	opts := ExecOpts{
		Stdout: &stdout,
	}

	exitCode, err := transport.Exec(ctx, []string{"sleep", "10"}, opts)

	require.NoError(t, err)        // Context cancellation returns exit code, not error
	assert.Equal(t, 124, exitCode) // Convention: 124 for timeout/cancellation
}

// TestLocalTransportPut tests file upload (local copy)
func TestLocalTransportPut(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	// Create temp directory for destination
	tmpDir := t.TempDir()
	dstPath := filepath.Join(tmpDir, "test.txt")

	// Source content
	content := "test file content\n"
	src := strings.NewReader(content)

	// Put file
	err := transport.Put(context.Background(), src, dstPath, 0o644)
	require.NoError(t, err)

	// Verify file exists and has correct content
	data, err := os.ReadFile(dstPath)
	require.NoError(t, err)
	assert.Equal(t, content, string(data))

	// Verify file permissions
	info, err := os.Stat(dstPath)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o644), info.Mode().Perm())
}

// TestLocalTransportPut_CreateParentDirs tests Put creates parent directories
func TestLocalTransportPut_CreateParentDirs(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	// Create temp directory
	tmpDir := t.TempDir()
	dstPath := filepath.Join(tmpDir, "subdir", "nested", "test.txt")

	// Source content
	content := "nested file\n"
	src := strings.NewReader(content)

	// Put file (should create parent dirs)
	err := transport.Put(context.Background(), src, dstPath, 0o644)
	require.NoError(t, err)

	// Verify file exists
	data, err := os.ReadFile(dstPath)
	require.NoError(t, err)
	assert.Equal(t, content, string(data))
}

// TestLocalTransportGet tests file download (local read)
func TestLocalTransportGet(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	// Create temp file
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "source.txt")
	content := "source file content\n"
	err := os.WriteFile(srcPath, []byte(content), 0o644)
	require.NoError(t, err)

	// Get file
	var dst bytes.Buffer
	err = transport.Get(context.Background(), srcPath, &dst)
	require.NoError(t, err)

	// Verify content
	assert.Equal(t, content, dst.String())
}

// TestLocalTransportGet_FileNotFound tests Get with missing file
func TestLocalTransportGet_FileNotFound(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	var dst bytes.Buffer
	err := transport.Get(context.Background(), "/nonexistent/file.txt", &dst)

	assert.Error(t, err)
	assert.True(t, os.IsNotExist(err))
}

// TestLocalTransportPut_ContextCancellation tests that Put respects context cancellation
func TestLocalTransportPut_ContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()
	dst := filepath.Join(tmpDir, "output.txt")

	transport := &LocalTransport{}
	defer transport.Close()

	// Create a slow reader that will be interrupted
	slowReader := &slowReader{data: make([]byte, 10*1024*1024)} // 10MB

	// Cancel context immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := transport.Put(ctx, slowReader, dst, 0o644)

	// Should return context.Canceled error
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// TestLocalTransportGet_ContextCancellation tests that Get respects context cancellation
func TestLocalTransportGet_ContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "large.txt")

	// Create a large file
	largeData := make([]byte, 10*1024*1024) // 10MB
	require.NoError(t, os.WriteFile(src, largeData, 0o644))

	transport := &LocalTransport{}
	defer transport.Close()

	// Create a slow writer that will be interrupted
	slowWriter := &slowWriter{}

	// Cancel context immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := transport.Get(ctx, src, slowWriter)

	// Should return context.Canceled error
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// slowReader simulates a slow io.Reader for testing context cancellation
type slowReader struct {
	data []byte
	pos  int
}

func (r *slowReader) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	// Read slowly to allow context cancellation
	n = copy(p, r.data[r.pos:r.pos+1])
	r.pos += n
	return n, nil
}

// slowWriter simulates a slow io.Writer for testing context cancellation
type slowWriter struct {
	written int
}

func (w *slowWriter) Write(p []byte) (n int, err error) {
	// Write slowly to allow context cancellation
	w.written += len(p)
	return len(p), nil
}

// TestLocalTransportClose tests Close is safe to call
func TestLocalTransportClose(t *testing.T) {
	transport := &LocalTransport{}

	// Close should not error
	err := transport.Close()
	assert.NoError(t, err)

	// Multiple closes should be safe
	err = transport.Close()
	assert.NoError(t, err)
}

// TestLocalTransportExec_EnvironmentOverride tests that decorator vars override parent env
func TestLocalTransportExec_EnvironmentOverride(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	// Set a base environment variable
	originalPath := os.Getenv("PATH")
	os.Setenv("TEST_OVERRIDE_VAR", "original_value")
	defer os.Unsetenv("TEST_OVERRIDE_VAR")

	var stdout bytes.Buffer
	opts := ExecOpts{
		Stdout: &stdout,
		Env: map[string]string{
			"TEST_OVERRIDE_VAR": "overridden_value", // Should override parent
			"TEST_NEW_VAR":      "new_value",        // Should be added
		},
	}

	exitCode, err := transport.Exec(context.Background(), []string{"sh", "-c", "echo $TEST_OVERRIDE_VAR:$TEST_NEW_VAR:$PATH"}, opts)

	require.NoError(t, err)
	assert.Equal(t, ExitSuccess, exitCode)

	output := stdout.String()
	// Should see overridden value, not original
	assert.Contains(t, output, "overridden_value")
	assert.NotContains(t, output, "original_value")
	// Should see new variable
	assert.Contains(t, output, "new_value")
	// Should still have PATH from parent environment
	assert.Contains(t, output, originalPath)
}

// TestLocalTransportExec_EnvironmentIsolation tests that only decorator vars are in opts.Env
func TestLocalTransportExec_EnvironmentIsolation(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	// Verify that when opts.Env is empty, command still has access to parent environment
	var stdout bytes.Buffer
	opts := ExecOpts{
		Stdout: &stdout,
		Env:    map[string]string{}, // Empty - should still inherit parent
	}

	exitCode, err := transport.Exec(context.Background(), []string{"sh", "-c", "echo $PATH"}, opts)

	require.NoError(t, err)
	assert.Equal(t, ExitSuccess, exitCode)
	// Should still have PATH because opts.Env is empty (cmd.Env stays nil)
	assert.NotEmpty(t, stdout.String())
}

// TestLocalTransportExec_StderrSeparate tests stdout and stderr are separate
func TestLocalTransportExec_StderrSeparate(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	var stdout, stderr bytes.Buffer
	opts := ExecOpts{
		Stdout: &stdout,
		Stderr: &stderr,
	}

	// Command that writes to both stdout and stderr
	exitCode, err := transport.Exec(context.Background(), []string{"sh", "-c", "echo stdout; echo stderr >&2"}, opts)

	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
	assert.Equal(t, "stdout\n", stdout.String())
	assert.Equal(t, "stderr\n", stderr.String())
}

// TestLocalTransportExec_NilWriters tests nil stdout/stderr are handled
func TestLocalTransportExec_NilWriters(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	opts := ExecOpts{
		Stdout: nil, // Should use os.Stdout
		Stderr: nil, // Should use os.Stderr
	}

	// Should not panic with nil writers
	exitCode, err := transport.Exec(context.Background(), []string{"echo", "test"}, opts)

	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
}

// TestLocalTransportExec_EmptyEnv tests empty environment map
func TestLocalTransportExec_EmptyEnv(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	var stdout bytes.Buffer
	opts := ExecOpts{
		Stdout: &stdout,
		Env:    map[string]string{}, // Empty env
	}

	// Should inherit parent environment
	exitCode, err := transport.Exec(context.Background(), []string{"sh", "-c", "echo $PATH"}, opts)

	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
	// PATH should be inherited from parent
	assert.NotEmpty(t, stdout.String())
}

// SDK callers and plan endpoints share streaming overwrite semantics.
func TestLocalTransportOpenFileWriter_StreamingOverwrite(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	tmpDir := t.TempDir()
	targetPath := filepath.Join(tmpDir, "output.txt")

	// Create initial file with content
	initialContent := "initial content\n"
	err := os.WriteFile(targetPath, []byte(initialContent), 0o644)
	require.NoError(t, err)

	// Open file for overwrite
	writer, err := transport.OpenFileWriter(context.Background(), targetPath, RedirectOverwrite, 0o644)
	require.NoError(t, err)

	// Write new content
	newContent := "new content\n"
	_, err = writer.Write([]byte(newContent))
	require.NoError(t, err)

	// Written bytes are visible before completion.
	data, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	assert.Equal(t, newContent, string(data), "output must stream before Close")

	// Completion closes the stream; it does not publish a replacement.
	err = writer.Close()
	require.NoError(t, err)

	// After Close(), file should have new content
	data, err = os.ReadFile(targetPath)
	require.NoError(t, err)
	assert.Equal(t, newContent, string(data), "File should have new content after Close()")

	entries, err := os.ReadDir(tmpDir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

// TestLocalTransportOpenFileWriter_Append tests append mode
func TestLocalTransportOpenFileWriter_Append(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	tmpDir := t.TempDir()
	targetPath := filepath.Join(tmpDir, "output.txt")

	// Create initial file with content
	initialContent := "line 1\n"
	err := os.WriteFile(targetPath, []byte(initialContent), 0o644)
	require.NoError(t, err)

	// Open file for append
	writer, err := transport.OpenFileWriter(context.Background(), targetPath, RedirectAppend, 0o644)
	require.NoError(t, err)

	// Write additional content
	appendContent := "line 2\n"
	_, err = writer.Write([]byte(appendContent))
	require.NoError(t, err)

	err = writer.Close()
	require.NoError(t, err)

	// File should have both lines
	data, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	assert.Equal(t, initialContent+appendContent, string(data))
}

// TestLocalTransportOpenFileWriter_CreateNew tests creating new file
func TestLocalTransportOpenFileWriter_CreateNew(t *testing.T) {
	transport := &LocalTransport{}
	defer transport.Close()

	tmpDir := t.TempDir()
	targetPath := filepath.Join(tmpDir, "newfile.txt")

	// Open non-existent file for overwrite
	writer, err := transport.OpenFileWriter(context.Background(), targetPath, RedirectOverwrite, 0o644)
	require.NoError(t, err)

	content := "new file content\n"
	_, err = writer.Write([]byte(content))
	require.NoError(t, err)

	err = writer.Close()
	require.NoError(t, err)

	// File should exist with content
	data, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	assert.Equal(t, content, string(data))

	// Verify permissions
	info, err := os.Stat(targetPath)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o644), info.Mode().Perm())
}
