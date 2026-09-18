package decorators

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/builtwithtofu/sigil/core/decorator"
	"github.com/google/go-cmp/cmp"
)

type sessionCallTracker struct {
	delegate  decorator.Session
	sessionID string
	puts      []string
	gets      []string
	runs      [][]string
	mu        sync.Mutex
}

func (s *sessionCallTracker) Run(ctx context.Context, argv []string, opts decorator.RunOpts) (decorator.Result, error) {
	s.mu.Lock()
	s.runs = append(s.runs, append([]string(nil), argv...))
	s.mu.Unlock()

	return s.delegate.Run(ctx, argv, opts)
}

func (s *sessionCallTracker) Put(ctx context.Context, data []byte, path string, perm fs.FileMode) error {
	s.mu.Lock()
	s.puts = append(s.puts, path)
	s.mu.Unlock()

	return s.delegate.Put(ctx, data, path, perm)
}

func (s *sessionCallTracker) Get(ctx context.Context, path string) ([]byte, error) {
	s.mu.Lock()
	s.gets = append(s.gets, path)
	s.mu.Unlock()

	return s.delegate.Get(ctx, path)
}

func (s *sessionCallTracker) Env() map[string]string {
	return s.delegate.Env()
}

func (s *sessionCallTracker) WithEnv(delta map[string]string) decorator.Session {
	return &sessionCallTracker{delegate: s.delegate.WithEnv(delta), sessionID: s.sessionID}
}

func (s *sessionCallTracker) WithWorkdir(dir string) decorator.Session {
	return &sessionCallTracker{delegate: s.delegate.WithWorkdir(dir), sessionID: s.sessionID}
}

func (s *sessionCallTracker) Cwd() string {
	return s.delegate.Cwd()
}

func (s *sessionCallTracker) Platform() string {
	return ""
}

func (s *sessionCallTracker) ID() string {
	return s.sessionID
}

func (s *sessionCallTracker) TransportScope() decorator.TransportScope {
	return s.delegate.TransportScope()
}

func (s *sessionCallTracker) Close() error {
	return s.delegate.Close()
}

func (s *sessionCallTracker) OpenFileOutput(ctx context.Context, path string, mode decorator.FileWriteMode, perm fs.FileMode) (decorator.Output, error) {
	return decorator.OpenFileOutput(ctx, s.delegate, path, mode, perm)
}

func (s *sessionCallTracker) snapshot() (puts, gets []string, runs [][]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	puts = append([]string(nil), s.puts...)
	gets = append([]string(nil), s.gets...)
	runs = make([][]string, 0, len(s.runs))
	for _, run := range s.runs {
		runs = append(runs, append([]string(nil), run...))
	}
	return puts, gets, runs
}

func TestFileSinkDecorator_DescriptorAndCaps(t *testing.T) {
	sink := &FileSinkDecorator{}

	desc := sink.Descriptor()
	if diff := cmp.Diff("file", desc.Path); diff != "" {
		t.Fatalf("descriptor path mismatch (-want +got):\n%s", diff)
	}

	gotCaps := sink.IOCaps()
	wantCaps := decorator.IOCaps{Read: true, Write: true, Append: true}
	if diff := cmp.Diff(wantCaps, gotCaps); diff != "" {
		t.Fatalf("iocaps mismatch (-want +got):\n%s", diff)
	}
}

func TestFileSinkDecorator_OverwriteWrite(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(tempDir, "out"), 0o700); err != nil {
		t.Fatal(err)
	}
	delegate := decorator.NewLocalSession().WithWorkdir(tempDir)
	tracker := &sessionCallTracker{delegate: delegate, sessionID: "local-tracker"}

	sink := &FileSinkDecorator{params: map[string]any{"path": "out/write.txt", "perm": 0o644}}
	ctx := decorator.ExecContext{Session: tracker, Context: context.Background()}

	writer, err := sink.OpenWrite(ctx, false)
	if err != nil {
		t.Fatalf("open write: %v", err)
	}

	n, err := writer.Write([]byte("first\n"))
	if err != nil {
		t.Fatalf("write first: %v", err)
	}
	if n != 6 {
		t.Fatalf("expected write 6 bytes, wrote %d", n)
	}
	if err := writer.Finish(context.Background()); err != nil {
		t.Fatalf("close write: %v", err)
	}

	path := filepath.Join(tempDir, "out", "write.txt")
	content, err := delegate.Get(context.Background(), path)
	if err != nil {
		t.Fatalf("read overwritten file: %v", err)
	}
	if diff := cmp.Diff("first\n", string(content)); diff != "" {
		t.Fatalf("overwrite content mismatch (-want +got):\n%s", diff)
	}

	puts, gets, _ := tracker.snapshot()
	if len(puts) != 0 {
		t.Fatalf("expected no direct put calls for streaming writer, got %d", len(puts))
	}
	if len(gets) != 0 {
		t.Fatalf("expected no read call for overwrite, got %d", len(gets))
	}
}

func TestFileSinkDecorator_AppendWrite(t *testing.T) {
	tempDir := t.TempDir()
	delegate := decorator.NewLocalSession().WithWorkdir(tempDir)
	tracker := &sessionCallTracker{delegate: delegate, sessionID: "append-tracker"}

	originalPath := filepath.Join(tempDir, "out", "append.txt")
	if err := delegate.Put(context.Background(), []byte("one\n"), originalPath, 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	sink := &FileSinkDecorator{params: map[string]any{"path": filepath.Join("out", "append.txt"), "perm": 0o644}}
	ctx := decorator.ExecContext{Session: tracker, Context: context.Background()}

	writer, err := sink.OpenWrite(ctx, true)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := writer.Write([]byte("two\n")); err != nil {
		t.Fatalf("write append: %v", err)
	}
	if err := writer.Finish(context.Background()); err != nil {
		t.Fatalf("close append: %v", err)
	}

	content, err := delegate.Get(context.Background(), filepath.Join(tempDir, "out", "append.txt"))
	if err != nil {
		t.Fatalf("read appended file: %v", err)
	}
	if diff := cmp.Diff("one\ntwo\n", string(content)); diff != "" {
		t.Fatalf("append content mismatch (-want +got):\n%s", diff)
	}

	puts, gets, _ := tracker.snapshot()
	if len(puts) != 0 {
		t.Fatalf("expected no direct put calls for append streaming writer, got %d", len(puts))
	}
	if len(gets) != 0 {
		t.Fatalf("expected no direct get calls for append streaming writer, got %d", len(gets))
	}
}

func TestFileSinkDecorator_ReadAndTransportContext(t *testing.T) {
	tempDir := t.TempDir()
	delegate := decorator.NewLocalSession().WithWorkdir(tempDir)
	tracker := &sessionCallTracker{delegate: delegate, sessionID: "transport-tracker"}

	if err := delegate.Put(context.Background(), []byte("content\n"), filepath.Join("in", "read.txt"), 0o644); err != nil {
		t.Fatalf("seed read file: %v", err)
	}

	sink := &FileSinkDecorator{params: map[string]any{"path": filepath.Join("in", "read.txt")}}
	ctx := decorator.ExecContext{Session: tracker, Context: context.Background()}

	reader, err := sink.OpenRead(ctx)
	if err != nil {
		t.Fatalf("open read: %v", err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read sink output: %v", err)
	}
	if diff := cmp.Diff("content\n", string(data)); diff != "" {
		t.Fatalf("read content mismatch (-want +got):\n%s", diff)
	}

	_, gets, runs := tracker.snapshot()
	if len(gets) != 1 {
		t.Fatalf("expected one direct get call for read path, got %d", len(gets))
	}
	if len(runs) != 0 {
		t.Fatalf("expected no session run calls for read path, got %d", len(runs))
	}

	if tracker.ID() == "" {
		t.Fatal("session ID must be non-empty")
	}
}

func TestFileSinkDecorator_MissingPath(t *testing.T) {
	sink := &FileSinkDecorator{}
	ctx := decorator.ExecContext{Session: decorator.NewLocalSession(), Context: context.Background()}

	if _, err := sink.OpenRead(ctx); err == nil {
		t.Fatal("expected missing path read error")
	}

	if _, err := sink.OpenWrite(ctx, false); err == nil {
		t.Fatal("expected missing path write error")
	}
}

func TestFileSinkDecorator_PermInt64(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(tempDir, "out"), 0o700); err != nil {
		t.Fatal(err)
	}
	delegate := decorator.NewLocalSession().WithWorkdir(tempDir)

	sink := &FileSinkDecorator{params: map[string]any{"path": "out/perm-int64.txt", "perm": int64(0o640)}}
	ctx := decorator.ExecContext{Session: delegate, Context: context.Background()}

	writer, err := sink.OpenWrite(ctx, false)
	if err != nil {
		t.Fatalf("open write with int64 perm: %v", err)
	}
	if _, err := writer.Write([]byte("perm\n")); err != nil {
		t.Fatalf("write with int64 perm: %v", err)
	}
	if err := writer.Finish(context.Background()); err != nil {
		t.Fatalf("close write with int64 perm: %v", err)
	}

	path := filepath.Join(tempDir, "out", "perm-int64.txt")
	content, err := delegate.Get(context.Background(), path)
	if err != nil {
		t.Fatalf("read file written with int64 perm: %v", err)
	}
	if diff := cmp.Diff("perm\n", string(content)); diff != "" {
		t.Fatalf("content mismatch (-want +got):\n%s", diff)
	}
}
