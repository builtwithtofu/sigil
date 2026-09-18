package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/builtwithtofu/sigil/core/sdk"
	"github.com/google/go-cmp/cmp"
)

type testSink struct {
	openErr    error
	readErr    error
	writer     *testSinkWriter
	reader     io.ReadCloser
	openCount  int
	readCount  int
	closeCount int
	openedOpts []sdk.SinkOpts
}

func (s *testSink) Caps() sdk.SinkCaps {
	return sdk.SinkCaps{Write: true, Append: true, Read: true}
}

func (s *testSink) OpenWrite(_ sdk.ExecutionContext, opts sdk.SinkOpts) (io.WriteCloser, error) {
	s.openCount++
	s.openedOpts = append(s.openedOpts, opts)
	if s.openErr != nil {
		return nil, s.openErr
	}
	if s.writer == nil {
		s.writer = &testSinkWriter{}
	}
	return s.writer, nil
}

func (s *testSink) OpenRead(_ sdk.ExecutionContext, _ sdk.SinkOpts) (io.ReadCloser, error) {
	s.readCount++
	if s.readErr != nil {
		return nil, s.readErr
	}
	if s.reader != nil {
		return &countingReadCloser{inner: s.reader, closeCount: &s.closeCount}, nil
	}
	return &countingReadCloser{inner: io.NopCloser(strings.NewReader("")), closeCount: &s.closeCount}, nil
}

func (s *testSink) Identity() (kind, identifier string) {
	return "test.sink", "capture"
}

type testSinkWriter struct {
	data      []byte
	failWrite bool
	failClose bool
}

func (w *testSinkWriter) Write(p []byte) (int, error) {
	if w.failWrite {
		return 0, errors.New("write failed")
	}
	w.data = append(w.data, p...)
	return len(p), nil
}

func (w *testSinkWriter) Close() error {
	if w.failClose {
		return errors.New("close failed")
	}
	return nil
}

type countingReadCloser struct {
	inner      io.ReadCloser
	closeCount *int
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	return r.inner.Read(p)
}

func (r *countingReadCloser) Close() error {
	*r.closeCount = *r.closeCount + 1
	return r.inner.Close()
}

type failAfterFirstRead struct {
	first []byte
	done  bool
}

func (r *failAfterFirstRead) Read(p []byte) (int, error) {
	if r.done {
		return 0, errors.New("mid-stream read failed")
	}
	r.done = true
	n := copy(p, r.first)
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

func (r *failAfterFirstRead) Close() error {
	return nil
}

type closeFailReadCloser struct {
	inner io.Reader
}

func (r *closeFailReadCloser) Read(p []byte) (int, error) {
	return r.inner.Read(p)
}

func (r *closeFailReadCloser) Close() error {
	return errors.New("close read failed")
}

// newExecutorWithStderr creates an executor with an injected stderr buffer for testing.
// Returns the executor, the stderr buffer, and a cleanup function.
func newExecutorWithStderr(t *testing.T) (*executor, *bytes.Buffer, func()) {
	t.Helper()
	stderrBuf := &bytes.Buffer{}

	e := &executor{
		sessions: newSessionRuntime(nil),
		workers:  nil,
		stderr:   stderrBuf,
	}

	e.workers = newShellWorkerPool(e.sessions)

	cleanup := func() {
		e.workers.Close()
		e.sessions.Close()
	}

	return e, stderrBuf, cleanup
}

func assertSinkErrorOutput(t *testing.T, output string, sinkErr SinkError) {
	t.Helper()

	if diff := cmp.Diff("test.sink (capture)", sinkErr.SinkID); diff != "" {
		t.Fatalf("sink id mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff("local", sinkErr.TransportID); diff != "" {
		t.Fatalf("transport id mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(true, sinkErr.Cause != nil); diff != "" {
		t.Fatalf("expected sink error cause (-want +got):\n%s", diff)
	}

	if diff := cmp.Diff(true, strings.Contains(output, "Error: "+sinkErr.Error())); diff != "" {
		t.Fatalf("stderr sink error output mismatch (-want +got):\n%s\noutput: %q", diff, output)
	}
}

func TestRedirectSinkOpenFailsBeforeCommandExecution(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "not-created.txt")
	sink := &testSink{openErr: errors.New("open failed")}
	e, stderrBuf, cleanup := newExecutorWithStderr(t)
	t.Cleanup(cleanup)

	execCtx := newExecutionContext(map[string]interface{}{}, e, context.Background())
	exitCode := e.executeRedirect(execCtx, &sdk.RedirectNode{
		Source: &sdk.CommandNode{
			Name: "@shell",
			Args: map[string]any{"command": "echo run > " + marker},
		},
		Sink: sink,
		Mode: sdk.RedirectOverwrite,
	}, nil)
	if diff := cmp.Diff(1, exitCode); diff != "" {
		t.Fatalf("exit code mismatch (-want +got):\n%s", diff)
	}

	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("marker file should not exist, stat err: %v", statErr)
	}
	if diff := cmp.Diff(1, sink.openCount); diff != "" {
		t.Fatalf("open count mismatch (-want +got):\n%s", diff)
	}
	assertSinkErrorOutput(t, stderrBuf.String(), SinkError{
		SinkID:      "test.sink (capture)",
		Operation:   "open",
		TransportID: "local",
		Cause:       sink.openErr,
	})
}

func TestRedirectSinkWriteFailureReturnsFailure(t *testing.T) {
	sink := &testSink{writer: &testSinkWriter{failWrite: true}}
	e, stderrBuf, cleanup := newExecutorWithStderr(t)
	t.Cleanup(cleanup)

	execCtx := newExecutionContext(map[string]interface{}{}, e, context.Background())
	exitCode := e.executeRedirect(execCtx, &sdk.RedirectNode{
		Source: &sdk.CommandNode{Name: "@shell", Args: map[string]any{"command": "echo fail"}},
		Sink:   sink,
		Mode:   sdk.RedirectOverwrite,
	}, nil)

	if diff := cmp.Diff(1, exitCode); diff != "" {
		t.Fatalf("exit code mismatch (-want +got):\n%s", diff)
	}
	assertSinkErrorOutput(t, stderrBuf.String(), SinkError{
		SinkID:      "test.sink (capture)",
		Operation:   "write",
		TransportID: "local",
		Cause:       errors.New("write failed"),
	})
}

func TestRedirectSinkCloseFailureReturnsFailure(t *testing.T) {
	sink := &testSink{writer: &testSinkWriter{failClose: true}}
	e, stderrBuf, cleanup := newExecutorWithStderr(t)
	t.Cleanup(cleanup)

	execCtx := newExecutionContext(map[string]interface{}{}, e, context.Background())
	exitCode := e.executeRedirect(execCtx, &sdk.RedirectNode{
		Source: &sdk.CommandNode{Name: "@shell", Args: map[string]any{"command": "echo close"}},
		Sink:   sink,
		Mode:   sdk.RedirectOverwrite,
	}, nil)

	if diff := cmp.Diff(1, exitCode); diff != "" {
		t.Fatalf("exit code mismatch (-want +got):\n%s", diff)
	}
	assertSinkErrorOutput(t, stderrBuf.String(), SinkError{
		SinkID:      "test.sink (capture)",
		Operation:   "finish",
		TransportID: "local",
		Cause:       errors.New("close failed"),
	})
}

func TestStderrCapture(t *testing.T) {
	t.Run("defaults to stdout", func(t *testing.T) {
		sink := &testSink{}
		e := &executor{sessions: newSessionRuntime(nil), workers: nil}
		t.Cleanup(func() { e.sessions.Close() })
		e.workers = newShellWorkerPool(e.sessions)
		t.Cleanup(func() { e.workers.Close() })

		execCtx := newExecutionContext(map[string]interface{}{}, e, context.Background())
		exitCode := e.executeRedirect(execCtx, &sdk.RedirectNode{
			Source: &sdk.CommandNode{
				Name: "@shell",
				Args: map[string]any{"command": "echo out && echo err 1>&2"},
			},
			Sink: sink,
			Mode: sdk.RedirectOverwrite,
		}, nil)

		if diff := cmp.Diff(0, exitCode); diff != "" {
			t.Fatalf("exit code mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff("out\n", string(sink.writer.data)); diff != "" {
			t.Fatalf("sink output mismatch (-want +got):\n%s", diff)
		}
		if len(sink.openedOpts) != 1 {
			t.Fatalf("expected one sink open, got %d", len(sink.openedOpts))
		}
		if diff := cmp.Diff(sdk.SinkStreamStdout, sink.openedOpts[0].Stream); diff != "" {
			t.Fatalf("sink stream mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("routes stderr when enabled", func(t *testing.T) {
		sink := &testSink{}
		e := &executor{sessions: newSessionRuntime(nil), workers: nil}
		t.Cleanup(func() { e.sessions.Close() })
		e.workers = newShellWorkerPool(e.sessions)
		t.Cleanup(func() { e.workers.Close() })

		execCtx := newExecutionContext(map[string]interface{}{}, e, context.Background())
		exitCode := e.executeRedirect(execCtx, &sdk.RedirectNode{
			Source: &sdk.CommandNode{
				Name: "@shell",
				Args: map[string]any{
					"command": "echo out && echo err 1>&2",
					"stderr":  true,
				},
			},
			Sink: sink,
			Mode: sdk.RedirectOverwrite,
		}, nil)

		if diff := cmp.Diff(0, exitCode); diff != "" {
			t.Fatalf("exit code mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff("err\n", string(sink.writer.data)); diff != "" {
			t.Fatalf("sink output mismatch (-want +got):\n%s", diff)
		}
		if len(sink.openedOpts) != 1 {
			t.Fatalf("expected one sink open, got %d", len(sink.openedOpts))
		}
		if diff := cmp.Diff(sdk.SinkStreamStderr, sink.openedOpts[0].Stream); diff != "" {
			t.Fatalf("sink stream mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestInputOperator(t *testing.T) {
	t.Parallel()

	t.Run("streams input from sink to command stdin", func(t *testing.T) {
		t.Parallel()

		sink := &testSink{reader: io.NopCloser(strings.NewReader("alpha\nbeta\n"))}
		e := &executor{sessions: newSessionRuntime(nil), workers: nil}
		t.Cleanup(func() { e.sessions.Close() })
		e.workers = newShellWorkerPool(e.sessions)
		t.Cleanup(func() { e.workers.Close() })

		execCtx := newExecutionContext(map[string]interface{}{}, e, context.Background())
		exitCode := e.executeRedirect(execCtx, &sdk.RedirectNode{
			Source: &sdk.CommandNode{Name: "@shell", Args: map[string]any{"command": "cat"}},
			Sink:   sink,
			Mode:   sdk.RedirectInput,
		}, nil)

		if diff := cmp.Diff(0, exitCode); diff != "" {
			t.Fatalf("exit code mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(1, sink.readCount); diff != "" {
			t.Fatalf("read count mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(0, sink.openCount); diff != "" {
			t.Fatalf("write open count mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("returns failure when source open fails", func(t *testing.T) {
		sink := &testSink{readErr: errors.New("open read failed")}
		e, stderrBuf, cleanup := newExecutorWithStderr(t)
		t.Cleanup(cleanup)

		execCtx := newExecutionContext(map[string]interface{}{}, e, context.Background())
		exitCode := e.executeRedirect(execCtx, &sdk.RedirectNode{
			Source: &sdk.CommandNode{Name: "@shell", Args: map[string]any{"command": "cat"}},
			Sink:   sink,
			Mode:   sdk.RedirectInput,
		}, nil)

		if diff := cmp.Diff(1, exitCode); diff != "" {
			t.Fatalf("exit code mismatch (-want +got):\n%s", diff)
		}
		assertSinkErrorOutput(t, stderrBuf.String(), SinkError{
			SinkID:      "test.sink (capture)",
			Operation:   "open",
			TransportID: "local",
			Cause:       sink.readErr,
		})
	})

	t.Run("returns failure when source read fails mid-stream", func(t *testing.T) {
		sink := &testSink{reader: &failAfterFirstRead{first: []byte("alpha\n")}}
		e := &executor{sessions: newSessionRuntime(nil), workers: nil}
		t.Cleanup(func() { e.sessions.Close() })
		e.workers = newShellWorkerPool(e.sessions)
		t.Cleanup(func() { e.workers.Close() })

		execCtx := newExecutionContext(map[string]interface{}{}, e, context.Background())
		exitCode := e.executeRedirect(execCtx, &sdk.RedirectNode{
			Source: &sdk.CommandNode{Name: "@shell", Args: map[string]any{"command": "cat"}},
			Sink:   sink,
			Mode:   sdk.RedirectInput,
		}, nil)

		if diff := cmp.Diff(1, exitCode); diff != "" {
			t.Fatalf("exit code mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(1, sink.readCount); diff != "" {
			t.Fatalf("read count mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(1, sink.closeCount); diff != "" {
			t.Fatalf("close count mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("returns failure when source close fails", func(t *testing.T) {
		sink := &testSink{reader: &closeFailReadCloser{inner: strings.NewReader("alpha\n")}}
		e, stderrBuf, cleanup := newExecutorWithStderr(t)
		t.Cleanup(cleanup)

		execCtx := newExecutionContext(map[string]interface{}{}, e, context.Background())
		exitCode := e.executeRedirect(execCtx, &sdk.RedirectNode{
			Source: &sdk.CommandNode{Name: "@shell", Args: map[string]any{"command": "cat"}},
			Sink:   sink,
			Mode:   sdk.RedirectInput,
		}, nil)

		if diff := cmp.Diff(1, exitCode); diff != "" {
			t.Fatalf("exit code mismatch (-want +got):\n%s", diff)
		}
		assertSinkErrorOutput(t, stderrBuf.String(), SinkError{
			SinkID:      "test.sink (capture)",
			Operation:   "close",
			TransportID: "local",
			Cause:       errors.New("close read failed"),
		})
	})
}

func TestSinkErrorFieldIntegrity(t *testing.T) {
	t.Parallel()

	errWithCause := SinkError{
		SinkID:      "test.sink (capture)",
		Operation:   "open",
		TransportID: "transport:A",
		Cause:       errors.New("boom"),
	}
	if diff := cmp.Diff("test.sink (capture)", errWithCause.SinkID); diff != "" {
		t.Fatalf("sink id mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff("open", errWithCause.Operation); diff != "" {
		t.Fatalf("operation mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff("transport:A", errWithCause.TransportID); diff != "" {
		t.Fatalf("transport id mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(true, errWithCause.Cause != nil); diff != "" {
		t.Fatalf("cause presence mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff("sink test.sink (capture) open failed on transport transport:A: boom", errWithCause.Error()); diff != "" {
		t.Fatalf("sink error string mismatch (-want +got):\n%s", diff)
	}

	errWithoutCause := SinkError{
		SinkID:      "test.sink (capture)",
		Operation:   "validate",
		TransportID: "local",
		Cause:       nil,
	}
	if diff := cmp.Diff("sink test.sink (capture) validate failed on transport local", errWithoutCause.Error()); diff != "" {
		t.Fatalf("sink error string mismatch without cause (-want +got):\n%s", diff)
	}
}
