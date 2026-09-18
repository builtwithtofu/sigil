// Package executor provides command execution and file transfer abstractions.
//
// The Transport interface enables Opal to execute commands in different environments
// (local, SSH, Docker, AWS SSM) while maintaining security guarantees and consistent
// error handling.
//
// # Environment Isolation
//
// Each Transport implementation provides its own base environment:
//   - LocalTransport: uses os.Environ() (local machine)
//   - SSHTransport: uses remote server's environment (automatic via SSH)
//   - DockerTransport: uses container's environment (automatic via docker exec)
//
// Decorator-added variables (ExecOpts.Env) are merged with the transport's base
// environment. Local environment variables NEVER leak to remote transports.
//
// # Exit Codes
//
// All transports use consistent POSIX-compatible exit codes:
//   - 0: Success
//   - 1-125: Command-specific failure
//   - 124: Timeout/cancellation
//   - 126: Permission denied (command cannot execute)
//   - 127: Command not found
//
// # Security
//
// All I/O flows through provided readers/writers which are scrubber-compatible.
// Transports cannot bypass the scrubber - they only receive pre-scrubbed writers.
package executor

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/builtwithtofu/sigil/core/decorator"
	"github.com/builtwithtofu/sigil/core/invariant"
)

// Exit Code Conventions (POSIX-compatible)
//
// These codes are used consistently across all Transport implementations
// to ensure uniform error handling regardless of execution environment.
const (
	ExitSuccess          = 0   // Command completed successfully
	ExitCommandFailed    = 1   // Generic command failure
	ExitTimeout          = 124 // Context cancelled/timeout (GNU timeout convention)
	ExitPermissionDenied = 126 // Command cannot execute (permission denied, not executable)
	ExitNotFound         = 127 // Command not found
)

// RedirectMode specifies how to open a file for redirection.
type RedirectMode int

const (
	RedirectOverwrite RedirectMode = iota // > (truncate file)
	RedirectAppend                        // >> (append to file)
	RedirectInput
)

// Transport abstracts command execution and file transfer for different environments.
// Implementations: LocalTransport (os/exec), SSHTransport (future), DockerTransport (future)
//
// Design principle: Transport is an implementation detail of decorators, not a first-class
// ExecutionContext member. Decorators like @ssh.connect wrap ExecutionContext and use
// Transport internally to redirect @shell commands to remote systems.
//
// Security guarantees:
//
//  1. I/O Isolation: All I/O flows through provided readers/writers which are scrubber-compatible.
//     Transport cannot bypass the scrubber - it only receives pre-scrubbed writers.
//
//  2. Environment Isolation: Each transport provides its OWN base environment and merges
//     ExecOpts.Env with THAT base. Local environment (os.Environ()) NEVER leaks to remote:
//     - LocalTransport: base = os.Environ() (local machine)
//     - SSHTransport: base = remote server's environment (automatic via SSH session)
//     - DockerTransport: base = container's environment (automatic via docker exec)
//     Only ExecOpts.Env (decorator-added variables) crosses transport boundaries.
type Transport interface {
	// Exec executes a command and returns exit code.
	// All I/O flows through provided readers/writers (scrubber-compatible).
	//
	// Exit codes:
	//   0   - Success
	//   1-126 - Command-specific failure
	//   127 - Command not found
	//   124 - Timeout/cancellation (context cancelled)
	//
	// Returns (exitCode, nil) for normal execution (even if command fails).
	// Returns (127, err) only for system errors (command not found, etc).
	Exec(ctx context.Context, argv []string, opts ExecOpts) (exitCode int, err error)

	// Put transfers a file/directory to the destination.
	// src: local file reader, dst: destination path, mode: file permissions
	//
	// For LocalTransport: copies to local filesystem
	// For SSHTransport: uploads via SFTP
	// For DockerTransport: uses docker cp
	Put(ctx context.Context, src io.Reader, dst string, mode fs.FileMode) error

	// Get retrieves a file/directory from the source.
	// src: source path, dst: local file writer
	//
	// For LocalTransport: reads from local filesystem
	// For SSHTransport: downloads via SFTP
	// For DockerTransport: uses docker cp
	Get(ctx context.Context, src string, dst io.Writer) error

	// OpenFileWriter opens a file for writing (for output redirection).
	// This enables `echo "hello" > file.txt` to work correctly in all contexts:
	//   - LocalTransport: opens local file
	//   - SSHTransport: opens remote file via `bash -c "cat > path"`
	//   - DockerTransport: opens file inside container via `docker exec sh -c "cat > path"`
	//
	// mode: RedirectOverwrite (>) or RedirectAppend (>>)
	// perm: file permissions (e.g., 0644)
	//
	// The returned WriteCloser MUST be closed by the caller.
	// Close() waits for the write operation to complete and returns any errors.
	OpenFileWriter(ctx context.Context, path string, mode RedirectMode, perm fs.FileMode) (io.WriteCloser, error)

	// OpenFileReader opens a file for reading (for input redirection).
	// This enables `cat < file.txt` to work correctly in all contexts:
	//   - LocalTransport: opens local file
	//   - SSHTransport: reads remote file via SSH
	//   - DockerTransport: reads file inside container
	//
	// The returned ReadCloser MUST be closed by the caller.
	OpenFileReader(ctx context.Context, path string) (io.ReadCloser, error)

	// Close cleans up transport resources (connections, sessions).
	// Safe to call multiple times.
	Close() error
}

// ExecOpts contains options for command execution.
type ExecOpts struct {
	Stdin  io.Reader         // Command stdin (may be nil)
	Stdout io.Writer         // Command stdout (scrubbed)
	Stderr io.Writer         // Command stderr (scrubbed)
	Env    map[string]string // Decorator-added environment variables (merged with transport's base environment)
	Dir    string            // Working directory (empty = current dir)
}

// Environment Isolation Contract:
//
// ExecOpts.Env contains ONLY decorator-added variables (e.g., from @env(X=y) decorator).
// Each Transport implementation merges these with its OWN base environment:
//
//   - LocalTransport: merges with os.Environ() (local machine's environment)
//   - SSHTransport: merges with remote server's environment (via SSH session)
//   - DockerTransport: merges with container's environment (via docker exec)
//
// This ensures local environment variables NEVER leak to remote transports.
// Only decorator-added variables (ExecOpts.Env) cross transport boundaries.

// MergeEnvironment merges base environment with decorator-added variables.
// Decorator variables override base environment variables if there are duplicates.
//
// This helper is used by LocalTransport. Other transports (SSH, Docker, SSM) handle
// environment merging differently because they use their own base environment:
//   - SSHTransport: uses session.Setenv() to add vars to remote environment
//   - DockerTransport: uses docker exec -e to add vars to container environment
//   - SSMTransport: uses SSM session environment + added vars
func MergeEnvironment(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}

	// Build map for deduplication
	envMap := make(map[string]string, len(base)+len(overrides))

	// Parse base environment
	for _, kv := range base {
		if idx := strings.IndexByte(kv, '='); idx > 0 {
			envMap[kv[:idx]] = kv[idx+1:]
		}
	}

	// Apply overrides (decorator-added variables take precedence)
	for k, v := range overrides {
		envMap[k] = v
	}

	// Convert back to slice
	result := make([]string, 0, len(envMap))
	for k, v := range envMap {
		result = append(result, k+"="+v)
	}
	return result
}

// LocalTransport implements Transport for local command execution using os/exec.
// This is the default transport used by executor.Command().
type LocalTransport struct {
	// No state needed for local execution
}

// Exec executes a command locally using os/exec.
func (t *LocalTransport) Exec(ctx context.Context, argv []string, opts ExecOpts) (int, error) {
	invariant.Precondition(len(argv) > 0, "argv cannot be empty")
	invariant.NotNil(ctx, "context")

	// Create command
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)

	// Set working directory
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}

	// Set environment
	if len(opts.Env) > 0 {
		// Merge local environment with decorator-added variables.
		//
		// Why os.Environ()? LocalTransport executes on the local machine, so we need
		// the local environment as the base. This includes:
		//   - PATH: to find commands (kubectl, npm, git, etc.)
		//   - HOME: for config files (~/.kube/config, ~/.npmrc, etc.)
		//   - USER, SHELL, LANG, etc.: standard system variables
		//
		// Without os.Environ(), commands would fail because they wouldn't be found in PATH.
		//
		// Security: This is LocalTransport-specific. Other transports use their OWN base:
		//   - SSHTransport: remote server's environment (NOT local os.Environ())
		//   - DockerTransport: container's environment (NOT local os.Environ())
		//   - SSMTransport: EC2 instance's environment (NOT local os.Environ())
		//
		// Only decorator-added variables (opts.Env) cross transport boundaries.
		// Local environment variables NEVER leak to remote transports.
		cmd.Env = MergeEnvironment(os.Environ(), opts.Env)
	}

	// Wire up I/O
	cmd.Stdin = opts.Stdin
	if opts.Stdout != nil {
		cmd.Stdout = opts.Stdout
	} else {
		cmd.Stdout = os.Stdout // Default to os.Stdout (locked down by CLI)
	}
	if opts.Stderr != nil {
		cmd.Stderr = opts.Stderr
	} else {
		cmd.Stderr = os.Stderr // Default to os.Stderr (locked down by CLI)
	}

	// Execute
	if err := cmd.Run(); err != nil {
		// Context cancellation/timeout
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return ExitTimeout, nil
		}
		// Process exited with non-zero code
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Check if process was killed by signal (exit code -1)
			// This happens when context is cancelled
			if exitErr.ExitCode() == -1 {
				return ExitTimeout, nil // Normalize to timeout code
			}
			return exitErr.ExitCode(), nil
		}
		// Classify exec errors by type
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			if os.IsPermission(pathErr.Err) {
				// Permission denied (file exists but not executable)
				return ExitPermissionDenied, err
			}
			if os.IsNotExist(pathErr.Err) && pathErr.Op != "chdir" {
				// Explicit path does not exist (but not a chdir error)
				return ExitNotFound, err
			}
		}
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			// Command not found in PATH
			return ExitNotFound, err
		}
		// Other errors (e.g., invalid working directory)
		return ExitCommandFailed, err
	}

	return ExitSuccess, nil
}

// copyWithContext copies data from src to dst while respecting context cancellation.
// Returns context.Canceled if context is cancelled during copy.
func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 32*1024) // 32KB buffer
	var written int64

	for {
		// Check context before each read
		select {
		case <-ctx.Done():
			return written, ctx.Err()
		default:
		}

		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[0:nr])
			if nw < 0 || nr < nw {
				nw = 0
				if ew == nil {
					ew = io.ErrShortWrite
				}
			}
			written += int64(nw)
			if ew != nil {
				return written, ew
			}
			if nr != nw {
				return written, io.ErrShortWrite
			}
		}
		if er != nil {
			if er != io.EOF {
				return written, er
			}
			break
		}
	}
	return written, nil
}

// Put copies a file to the local filesystem.
// Creates parent directories if they don't exist.
func (t *LocalTransport) Put(ctx context.Context, src io.Reader, dst string, mode fs.FileMode) error {
	invariant.NotNil(ctx, "context")
	invariant.NotNil(src, "source reader")
	invariant.Precondition(dst != "", "destination path cannot be empty")

	// Create parent directories
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Create destination file
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	// Copy content with context awareness
	if _, err := copyWithContext(ctx, f, src); err != nil {
		return err
	}

	return nil
}

// Get reads a file from the local filesystem.
func (t *LocalTransport) Get(ctx context.Context, src string, dst io.Writer) error {
	invariant.NotNil(ctx, "context")
	invariant.Precondition(src != "", "source path cannot be empty")
	invariant.NotNil(dst, "destination writer")

	// Open source file
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	// Copy content with context awareness
	if _, err := copyWithContext(ctx, dst, f); err != nil {
		return err
	}

	return nil
}

// OpenFileWriter shares session-owned streaming semantics with plan execution.
func (t *LocalTransport) OpenFileWriter(ctx context.Context, path string, mode RedirectMode, perm fs.FileMode) (io.WriteCloser, error) {
	writeMode := decorator.FileTruncate
	switch mode {
	case RedirectOverwrite:
	case RedirectAppend:
		writeMode = decorator.FileAppend
	default:
		return nil, errors.New("invalid file output mode")
	}
	output, err := decorator.OpenFileOutput(ctx, decorator.NewLocalSession(), path, writeMode, perm)
	if err != nil {
		return nil, err
	}
	return decorator.CloseableOutput(ctx, output), nil
}

// OpenFileReader opens a local file for reading (for input redirection).
func (t *LocalTransport) OpenFileReader(ctx context.Context, path string) (io.ReadCloser, error) {
	invariant.NotNil(ctx, "context")
	invariant.Precondition(path != "", "path cannot be empty")

	return os.Open(path)
}

// Close is a no-op for LocalTransport (no resources to clean up).
// Safe to call multiple times.
func (t *LocalTransport) Close() error {
	return nil
}
