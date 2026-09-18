package executor

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"

	"github.com/builtwithtofu/sigil/core/decorator"
	"github.com/builtwithtofu/sigil/core/planfmt"
	_ "github.com/builtwithtofu/sigil/runtime/decorators"
	"github.com/google/go-cmp/cmp"
)

type sinkCaptureRecord struct {
	openCount  int
	sessionIDs []string
	output     bytes.Buffer
}

type sinkCaptureStore struct {
	mu      sync.Mutex
	records map[string]*sinkCaptureRecord
}

var testSinkStore = &sinkCaptureStore{records: map[string]*sinkCaptureRecord{}}

func (s *sinkCaptureStore) reset(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[id] = &sinkCaptureRecord{}
}

func (s *sinkCaptureStore) withRecord(id string, f func(*sinkCaptureRecord)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		record = &sinkCaptureRecord{}
		s.records[id] = record
	}
	f(record)
}

func (s *sinkCaptureStore) snapshot(id string) sinkCaptureRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return sinkCaptureRecord{}
	}
	copyRecord := sinkCaptureRecord{
		openCount:  record.openCount,
		sessionIDs: append([]string(nil), record.sessionIDs...),
	}
	copyRecord.output.Write(record.output.Bytes())
	return copyRecord
}

type captureSinkDecorator struct {
	id string
}

func (d *captureSinkDecorator) Descriptor() decorator.Descriptor {
	return decorator.NewDescriptor("test.capture.sink").
		Summary("Test-only sink decorator for transport capture assertions").
		Roles(decorator.RoleEndpoint).
		ParamString("command", "Capture sink identifier").
		Required().
		Done().
		Build()
}

func (d *captureSinkDecorator) IOCaps() decorator.IOCaps {
	return decorator.IOCaps{Write: true, Append: true}
}

func (d *captureSinkDecorator) OpenRead(ctx decorator.ExecContext) (io.ReadCloser, error) {
	return nil, nil
}

func (d *captureSinkDecorator) OpenWrite(ctx decorator.ExecContext, appendMode bool) (decorator.Output, error) {
	testSinkStore.withRecord(d.id, func(record *sinkCaptureRecord) {
		record.openCount++
		record.sessionIDs = append(record.sessionIDs, ctx.Session.ID())
		if !appendMode {
			record.output.Reset()
		}
	})
	return decorator.StreamingOutput(&captureSinkWriter{id: d.id}), nil
}

func (d *captureSinkDecorator) WithParams(params map[string]any) decorator.IO {
	id, _ := params["command"].(string)
	return &captureSinkDecorator{id: id}
}

type captureSinkWriter struct {
	id string
}

func (w *captureSinkWriter) Write(p []byte) (int, error) {
	testSinkStore.withRecord(w.id, func(record *sinkCaptureRecord) {
		_, _ = record.output.Write(p)
	})
	return len(p), nil
}

func (w *captureSinkWriter) Close() error {
	return nil
}

var registerCaptureSinkOnce sync.Once

func registerCaptureSinkDecorator(t *testing.T) {
	t.Helper()
	var registerErr error
	registerCaptureSinkOnce.Do(func() {
		registerErr = decorator.Register("test.capture.sink", &captureSinkDecorator{})
	})
	if registerErr != nil {
		t.Fatalf("register test.capture.sink: %v", registerErr)
	}
}

func TestRedirectSinkUsesSourceTransportContext(t *testing.T) {
	t.Parallel()
	registerCaptureSinkDecorator(t)

	id := t.TempDir() + "/capture-A"
	testSinkStore.reset(id)

	plan := &planfmt.Plan{Target: "redirect-source-transport", Transports: localTestTransports("transport:A"), Steps: []planfmt.Step{{
		ID: 1,
		Tree: &planfmt.RedirectNode{
			Source: &planfmt.CommandNode{
				Decorator:   "@shell",
				TransportID: "transport:A",
				Args:        []planfmt.Arg{{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "echo routed"}}},
			},
			Target: planfmt.EndpointSpec{
				TransportID: "transport:A",
				Decorator:   "@test.capture.sink",
				Args:        []planfmt.Arg{{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: id}}},
			},
			Mode: planfmt.RedirectOverwrite,
		},
	}}}

	result, err := ExecutePlan(context.Background(), plan, Config{sessionFactory: scopedLocalSessionFactory}, testVault())
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if diff := cmp.Diff(0, result.ExitCode); diff != "" {
		t.Fatalf("exit code mismatch (-want +got):\n%s", diff)
	}

	record := testSinkStore.snapshot(id)
	if diff := cmp.Diff(1, record.openCount); diff != "" {
		t.Fatalf("sink open count mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"transport:A"}, record.sessionIDs); diff != "" {
		t.Fatalf("session routing mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff("routed\n", record.output.String()); diff != "" {
		t.Fatalf("sink output mismatch (-want +got):\n%s", diff)
	}
}

func TestRedirectSinkInheritsWrapperTransportContext(t *testing.T) {
	t.Parallel()
	registerSessionBoundaryDecorator(t)
	registerCaptureSinkDecorator(t)

	id := t.TempDir() + "/capture-boundary"
	testSinkStore.reset(id)

	plan := &planfmt.Plan{Target: "redirect-wrapper-transport", Transports: localTestTransports("transport:boundary"), Steps: []planfmt.Step{{
		ID: 1,
		Tree: &planfmt.CommandNode{
			Decorator: "@test.session.boundary",
			Args:      []planfmt.Arg{{Key: "id", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "transport:boundary"}}},
			Block: []planfmt.Step{{
				ID: 2,
				Tree: &planfmt.RedirectNode{
					Source: &planfmt.CommandNode{
						Decorator: "@shell",
						Args:      []planfmt.Arg{{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "echo routed"}}},
					},
					Target: planfmt.EndpointSpec{
						Decorator: "@test.capture.sink",
						Args:      []planfmt.Arg{{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: id}}},
					},
					Mode: planfmt.RedirectOverwrite,
				},
			}},
		},
	}}}

	result, err := ExecutePlan(context.Background(), plan, Config{sessionFactory: scopedLocalSessionFactory}, testVault())
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if diff := cmp.Diff(0, result.ExitCode); diff != "" {
		t.Fatalf("exit code mismatch (-want +got):\n%s", diff)
	}

	record := testSinkStore.snapshot(id)
	if diff := cmp.Diff(1, record.openCount); diff != "" {
		t.Fatalf("sink open count mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"transport:boundary"}, record.sessionIDs); diff != "" {
		t.Fatalf("session routing mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff("routed\n", record.output.String()); diff != "" {
		t.Fatalf("sink output mismatch (-want +got):\n%s", diff)
	}
}
