package sdk_test

import (
	"io"
	"testing"

	"github.com/builtwithtofu/sigil/core/sdk"
	"github.com/google/go-cmp/cmp"
)

// mockSink is a test sink with configurable capabilities
type mockSink struct {
	caps     sdk.SinkCaps
	kind     string
	identity string
}

func (m *mockSink) Caps() sdk.SinkCaps {
	return m.caps
}

func (m *mockSink) OpenWrite(_ sdk.ExecutionContext, _ sdk.SinkOpts) (io.WriteCloser, error) {
	return nil, nil
}

func (m *mockSink) OpenRead(_ sdk.ExecutionContext, _ sdk.SinkOpts) (io.ReadCloser, error) {
	return nil, nil
}

func (m *mockSink) Open(_ sdk.ExecutionContext, _ sdk.RedirectMode, _ map[string]any) (io.WriteCloser, error) {
	return nil, nil
}

func (m *mockSink) Identity() (kind, identifier string) {
	return m.kind, m.identity
}

// TestValidateSinkForWrite_Overwrite_Success tests overwrite validation passes when supported
func TestValidateSinkForWrite_Overwrite_Success(t *testing.T) {
	t.Parallel()
	sink := &mockSink{
		caps:     sdk.SinkCaps{Write: true},
		kind:     "test.sink",
		identity: "test-path",
	}

	err := sdk.ValidateSinkForWrite(sink, sdk.RedirectOverwrite)
	if err != nil {
		t.Errorf("expected no error for overwrite-capable sink, got: %v", err)
	}
}

// TestValidateSinkForWrite_Overwrite_Failure tests overwrite validation fails when not supported
func TestValidateSinkForWrite_Overwrite_Failure(t *testing.T) {
	t.Parallel()
	sink := &mockSink{
		caps:     sdk.SinkCaps{Write: false},
		kind:     "test.sink",
		identity: "test-path",
	}

	err := sdk.ValidateSinkForWrite(sink, sdk.RedirectOverwrite)
	if err == nil {
		t.Fatal("expected error for non-overwrite-capable sink, got nil")
	}

	capsErr, ok := err.(*sdk.SinkCapabilityError)
	if !ok {
		t.Fatalf("expected SinkCapabilityError, got %T", err)
	}

	expected := &sdk.SinkCapabilityError{
		SinkKind:    "test.sink",
		SinkID:      "test-path",
		RequestedOp: "overwrite (>)",
		MissingCaps: []string{"Write"},
	}
	if diff := cmp.Diff(expected, capsErr); diff != "" {
		t.Errorf("error mismatch (-want +got):\n%s", diff)
	}
}

// TestValidateSinkForWrite_Append_Success tests append validation passes when supported
func TestValidateSinkForWrite_Append_Success(t *testing.T) {
	t.Parallel()
	sink := &mockSink{
		caps:     sdk.SinkCaps{Append: true},
		kind:     "test.sink",
		identity: "test-path",
	}

	err := sdk.ValidateSinkForWrite(sink, sdk.RedirectAppend)
	if err != nil {
		t.Errorf("expected no error for append-capable sink, got: %v", err)
	}
}

// TestValidateSinkForWrite_Append_Failure tests append validation fails when not supported
func TestValidateSinkForWrite_Append_Failure(t *testing.T) {
	t.Parallel()
	sink := &mockSink{
		caps:     sdk.SinkCaps{Append: false},
		kind:     "test.sink",
		identity: "test-path",
	}

	err := sdk.ValidateSinkForWrite(sink, sdk.RedirectAppend)
	if err == nil {
		t.Fatal("expected error for non-append-capable sink, got nil")
	}

	capsErr, ok := err.(*sdk.SinkCapabilityError)
	if !ok {
		t.Fatalf("expected SinkCapabilityError, got %T", err)
	}

	expected := &sdk.SinkCapabilityError{
		SinkKind:    "test.sink",
		SinkID:      "test-path",
		RequestedOp: "append (>>)",
		MissingCaps: []string{"Append"},
	}
	if diff := cmp.Diff(expected, capsErr); diff != "" {
		t.Errorf("error mismatch (-want +got):\n%s", diff)
	}
}

// TestValidateSinkForRead_Success tests read validation passes when supported
func TestValidateSinkForRead_Success(t *testing.T) {
	t.Parallel()
	sink := &mockSink{
		caps:     sdk.SinkCaps{Read: true},
		kind:     "test.sink",
		identity: "test-path",
	}

	err := sdk.ValidateSinkForRead(sink)
	if err != nil {
		t.Errorf("expected no error for read-capable sink, got: %v", err)
	}
}

// TestValidateSinkForRead_Failure tests read validation fails when not supported
func TestValidateSinkForRead_Failure(t *testing.T) {
	t.Parallel()
	sink := &mockSink{
		caps:     sdk.SinkCaps{Read: false},
		kind:     "test.sink",
		identity: "test-path",
	}

	err := sdk.ValidateSinkForRead(sink)
	if err == nil {
		t.Fatal("expected error for non-read-capable sink, got nil")
	}

	capsErr, ok := err.(*sdk.SinkCapabilityError)
	if !ok {
		t.Fatalf("expected SinkCapabilityError, got %T", err)
	}

	expected := &sdk.SinkCapabilityError{
		SinkKind:    "test.sink",
		SinkID:      "test-path",
		RequestedOp: "read (<)",
		MissingCaps: []string{"Read"},
	}
	if diff := cmp.Diff(expected, capsErr); diff != "" {
		t.Errorf("error mismatch (-want +got):\n%s", diff)
	}
}

// TestSinkCapabilityError_ErrorMessage tests error message formatting
func TestSinkCapabilityError_ErrorMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      *sdk.SinkCapabilityError
		expected string
	}{
		{
			name: "with missing caps",
			err: &sdk.SinkCapabilityError{
				SinkKind:    "s3.object",
				SinkID:      "bucket/key",
				RequestedOp: "append (>>)",
				MissingCaps: []string{"Append"},
			},
			expected: "append (>>) not supported by sink s3.object (bucket/key) - missing: Append",
		},
		{
			name: "without missing caps",
			err: &sdk.SinkCapabilityError{
				SinkKind:    "http.post",
				SinkID:      "https://example.com/webhook",
				RequestedOp: "overwrite (>)",
				MissingCaps: nil,
			},
			expected: "overwrite (>) not supported by sink http.post (https://example.com/webhook)",
		},
		{
			name: "multiple missing caps",
			err: &sdk.SinkCapabilityError{
				SinkKind:    "custom.sink",
				SinkID:      "custom-identifier",
				RequestedOp: "append (>>)",
				MissingCaps: []string{"Append", "Streaming"},
			},
			expected: "append (>>) not supported by sink custom.sink (custom-identifier) - missing: Append, Streaming",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.expected, tt.err.Error()); diff != "" {
				t.Errorf("error message mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestFsPathSink_Identity tests that FsPathSink returns correct identity
func TestFsPathSink_Identity(t *testing.T) {
	t.Parallel()
	sink := sdk.FsPathSink{Path: "/tmp/output.txt"}

	kind, id := sink.Identity()
	if kind != "fs.file" {
		t.Errorf("expected kind 'fs.file', got %q", kind)
	}
	if id != "/tmp/output.txt" {
		t.Errorf("expected id '/tmp/output.txt', got %q", id)
	}
}
