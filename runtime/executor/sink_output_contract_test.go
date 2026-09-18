package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/builtwithtofu/sigil/core/decorator"
	"github.com/stretchr/testify/require"
)

// Faults live at the session's output boundary; planning, shell production and
// the file output itself remain real collaborators.
type outputFault struct {
	short                         bool
	writeErr, finishErr, abortErr error
	afterWrite                    func()
	opens                         atomic.Int32
}
type faultOutputSession struct {
	decorator.Session
	fault *outputFault
}

func (s *faultOutputSession) WithWorkdir(dir string) decorator.Session {
	return &faultOutputSession{s.Session.WithWorkdir(dir), s.fault}
}

func (s *faultOutputSession) WithEnv(env map[string]string) decorator.Session {
	return &faultOutputSession{s.Session.WithEnv(env), s.fault}
}

func (s *faultOutputSession) OpenFileOutput(ctx context.Context, path string, mode decorator.FileWriteMode, perm fs.FileMode) (decorator.Output, error) {
	output, err := decorator.OpenFileOutput(ctx, s.Session, path, mode, perm)
	if err != nil {
		return nil, err
	}
	s.fault.opens.Add(1)
	return &faultFileOutput{Output: output, fault: s.fault}, nil
}

type faultFileOutput struct {
	decorator.Output
	fault *outputFault
}

func (w *faultFileOutput) Write(p []byte) (int, error) {
	if w.fault.short {
		p = p[:len(p)/2]
	}
	n, err := w.Output.Write(p)
	if w.fault.afterWrite != nil {
		w.fault.afterWrite()
	}
	return n, errors.Join(err, w.fault.writeErr)
}

func (w *faultFileOutput) Finish(ctx context.Context) error {
	return errors.Join(w.Output.Finish(ctx), w.fault.finishErr)
}

func (w *faultFileOutput) Abort(ctx context.Context) error {
	return errors.Join(w.Output.Abort(ctx), w.fault.abortErr)
}

func runFaultyOutput(t *testing.T, ctx context.Context, source string, fault *outputFault) (*ExecutionResult, string) {
	t.Helper()
	p, v := planSinkScript(t, source)
	var stderr bytes.Buffer
	result, err := ExecutePlan(ctx, p, Config{Stderr: &stderr, sessionFactory: func(string) (decorator.Session, error) {
		return &faultOutputSession{decorator.NewLocalSession(), fault}, nil
	}}, v)
	require.NoError(t, err)
	return result, stderr.String()
}

func TestSinkWriteAndShortWriteFailuresDiscardAtomicStaging(t *testing.T) {
	diskFull := errors.New("disk full")
	for _, tc := range []struct {
		name  string
		fault *outputFault
		cause error
	}{
		{"write", &outputFault{writeErr: diskFull}, diskFull},
		{"short write", &outputFault{short: true}, io.ErrShortWrite},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "artifact")
			require.NoError(t, os.WriteFile(path, []byte("old"), 0o600))
			result, diagnostics := runFaultyOutput(t, context.Background(), fmt.Sprintf("printf replacement > @file(%q, atomic=true)", path), tc.fault)
			require.NotZero(t, result.ExitCode, diagnostics)
			require.ErrorIs(t, errors.Join(result.Errors...), tc.cause)
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, "old", string(data))
			files, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Len(t, files, 1)
		})
	}
}

func TestSinkCancellationDiscardsAtomicStaging(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o600))
	result, _ := runFaultyOutput(t, ctx, fmt.Sprintf("printf replacement > @file(%q, atomic=true)", path), &outputFault{afterWrite: cancel})
	require.Equal(t, decorator.ExitCanceled, result.ExitCode)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "old", string(data))
	files, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, files, 1)
}

func TestUnknownPublicationStopsAutomaticRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact")
	fault := &outputFault{finishErr: decorator.ErrPublicationUnknown}
	result, diagnostics := runFaultyOutput(t, context.Background(), fmt.Sprintf("@exec.retry(delay=0s, times=3) { printf payload > @file(%q, atomic=true) }", path), fault)
	require.NotZero(t, result.ExitCode)
	require.EqualValues(t, 1, fault.opens.Load(), "uncertain publication was replayed")
	require.ErrorIs(t, errors.Join(result.Errors...), decorator.ErrPublicationUnknown)
	require.Contains(t, diagnostics, "reconcile destination before retrying")
}

func TestProducerAndCleanupFailuresAreBothReported(t *testing.T) {
	cleanupErr := errors.New("cleanup failed")
	path := filepath.Join(t.TempDir(), "artifact")
	result, diagnostics := runFaultyOutput(t, context.Background(), fmt.Sprintf("sh -c 'printf partial; exit 23' > @file(%q)", path), &outputFault{abortErr: cleanupErr})
	require.NotZero(t, result.ExitCode)
	require.ErrorIs(t, errors.Join(result.Errors...), cleanupErr)
	require.Contains(t, diagnostics, "producer exited with status 23")
}
