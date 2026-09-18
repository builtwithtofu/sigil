package decorator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// Output separates successful completion from cleanup after failed production.
// Abort is idempotent and never publishes staged data or rolls back streamed bytes.
// Finish and Abort must release resources even when they return an error.
type Output interface {
	io.Writer
	Finish(context.Context) error
	Abort(context.Context) error
}

// ErrPublicationUnknown means output may have been accepted without acknowledgement.
// Reconcile the destination before replaying the operation.
var ErrPublicationUnknown = errors.New("publication outcome unknown; reconcile destination before retrying")

// FileWriteMode specifies the effect requested of a session filesystem.
type FileWriteMode uint8

const (
	FileTruncate FileWriteMode = iota
	FileAppend
	FileReplaceOnSuccess
)

// FileWriter is an optional session capability. Implementations open before returning
// and interpret paths on their own filesystem. Unsupported sessions fail closed.
type FileWriter interface {
	OpenFileOutput(context.Context, string, FileWriteMode, fs.FileMode) (Output, error)
}

func OpenFileOutput(ctx context.Context, session Session, path string, mode FileWriteMode, perm fs.FileMode) (Output, error) {
	if session == nil {
		return nil, errors.New("execution context missing session")
	}
	writer, ok := session.(FileWriter)
	if !ok {
		return nil, fmt.Errorf("session %q does not support streaming file output", session.ID())
	}
	return writer.OpenFileOutput(ctx, path, mode, perm)
}

func (s *LocalSession) OpenFileOutput(ctx context.Context, path string, mode FileWriteMode, perm fs.FileMode) (Output, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, errors.New("file path is empty")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.cwd, path)
	}
	flags := os.O_CREATE | os.O_WRONLY
	switch mode {
	case FileTruncate:
		flags |= os.O_TRUNC
	case FileAppend:
		flags |= os.O_APPEND
	case FileReplaceOnSuccess:
		if runtime.GOOS == "windows" {
			return nil, errors.New("atomic file replacement is not supported on Windows")
		}
		if info, err := os.Lstat(path); err == nil {
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("atomic replacement requires a regular file: %s", path)
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		f, err := os.CreateTemp(filepath.Dir(path), ".sigil-output-*")
		if err != nil {
			return nil, err
		}
		return watchLocalFileOutput(&localFileOutput{file: f, ctx: ctx, destination: path, staging: f.Name(), perm: perm}), nil
	default:
		return nil, errors.New("file publication mode is not supported")
	}
	f, err := openFileForOutput(ctx, path, flags, perm)
	if err != nil {
		return nil, err
	}
	return watchLocalFileOutput(&localFileOutput{file: f, ctx: ctx}), nil
}

type localFileOutput struct {
	mu          sync.Mutex
	file        *os.File
	ctx         context.Context
	closed      bool
	err         error
	destination string
	staging     string
	perm        fs.FileMode
	stopCancel  func() bool
	closeOnce   sync.Once
	closeErr    error
}

func watchLocalFileOutput(w *localFileOutput) *localFileOutput {
	w.stopCancel = context.AfterFunc(w.ctx, func() { _ = w.closeFile() })
	return w
}

// Closing must not acquire mu: a blocked Write holds it. os.File.Close
// interrupts pollable I/O; sync.Once shares the close outcome with cleanup.
func (w *localFileOutput) closeFile() error {
	w.closeOnce.Do(func() { w.closeErr = w.file.Close() })
	return w.closeErr
}

func (w *localFileOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, fs.ErrClosed
	}
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.file.Write(p)
	if err != nil {
		err = errors.Join(err, w.ctx.Err())
	}
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	w.err = errors.Join(w.err, err)
	return n, err
}

func (w *localFileOutput) Finish(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.err
	}
	w.closed = true
	w.err = errors.Join(w.err, ctx.Err(), w.ctx.Err())
	if w.staging != "" && w.err == nil {
		w.err = w.file.Chmod(w.perm)
	}
	w.stopCancel()
	w.err = errors.Join(w.err, w.closeFile())
	// This final check is the publication decision. Rename cannot be cancelled;
	// cancellation racing after this decision cannot roll back a replacement.
	if w.err == nil {
		w.err = errors.Join(ctx.Err(), w.ctx.Err())
	}
	if w.staging != "" {
		if w.err == nil {
			w.err = os.Rename(w.staging, w.destination)
		}
		if w.err == nil {
			w.staging = ""
		} else {
			w.err = errors.Join(w.err, w.removeStaging())
		}
	}
	return w.err
}

func (w *localFileOutput) Abort(_ context.Context) error {
	w.stopCancel()
	closeErr := w.closeFile()
	w.mu.Lock()
	defer w.mu.Unlock()
	var err error
	if !w.closed {
		w.closed = true
		err = closeErr
		w.err = errors.Join(w.err, fs.ErrClosed, err)
	}
	return errors.Join(err, w.removeStaging())
}

func (w *localFileOutput) removeStaging() error {
	if w.staging == "" {
		return nil
	}
	err := os.Remove(w.staging)
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	if err == nil {
		w.staging = ""
	}
	return err
}

// outputCloser adapts existing streaming callers; the executor uses Output directly.
type outputCloser struct {
	Output
	ctx context.Context
}

func (w *outputCloser) Close() error { return w.Finish(w.ctx) }
func CloseableOutput(ctx context.Context, output Output) io.WriteCloser {
	return &outputCloser{Output: output, ctx: ctx}
}
