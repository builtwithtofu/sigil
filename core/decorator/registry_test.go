package decorator

import (
	"fmt"
	"io"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestAutoInference verifies roles are auto-inferred from interfaces
func TestAutoInference(t *testing.T) {
	r := NewRegistry()

	// Register a value decorator
	varDec := &mockValueDecorator{path: "var"}
	err := r.register("var", varDec)
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	// Lookup and verify role was inferred
	entry, ok := r.Lookup("var")
	if !ok {
		t.Fatal("decorator not found")
	}

	if len(entry.Roles) != 1 {
		t.Fatalf("expected 1 role, got %d", len(entry.Roles))
	}

	if entry.Roles[0] != RoleProvider {
		t.Errorf("expected RoleProvider, got %v", entry.Roles[0])
	}
}

func TestRegisterCompilesDecoderCache(t *testing.T) {
	r := NewRegistry()

	dec := &mockValueDecorator{path: "cache.test"}
	if err := r.register("cache.test", dec); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	entry, ok := r.Lookup("cache.test")
	if !ok {
		t.Fatal("decorator not found")
	}

	if entry.decoder == nil {
		t.Fatal("entry decoder cache is nil")
	}
}

// TestMultiRoleInference verifies multi-role decorators
func TestMultiRoleInference(t *testing.T) {
	r := NewRegistry()

	// Register a decorator that implements both Value and Endpoint
	s3Dec := &mockMultiRoleDecorator{path: "aws.s3.object"}
	err := r.register("aws.s3.object", s3Dec)
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	// Lookup and verify both roles were inferred
	entry, ok := r.Lookup("aws.s3.object")
	if !ok {
		t.Fatal("decorator not found")
	}

	if len(entry.Roles) != 2 {
		t.Fatalf("expected 2 roles, got %d", len(entry.Roles))
	}

	hasProvider := false
	hasEndpoint := false
	for _, role := range entry.Roles {
		if role == RoleProvider {
			hasProvider = true
		}
		if role == RoleEndpoint {
			hasEndpoint = true
		}
	}

	if !hasProvider {
		t.Error("missing RoleProvider")
	}
	if !hasEndpoint {
		t.Error("missing RoleEndpoint")
	}
}

// TestAllRoleInference verifies all role types can be inferred
func TestAllRoleInference(t *testing.T) {
	r := NewRegistry()

	tests := []struct {
		name     string
		impl     Decorator
		wantRole Role
	}{
		{"value", &mockValueDecorator{path: "test.value"}, RoleProvider},
		{"exec", &mockExecDecorator{path: "test.exec"}, RoleWrapper},
		{"transport", &mockTransportDecorator{path: "test.transport"}, RoleBoundary},
		{"endpoint", &mockIODecorator{path: "test.endpoint"}, RoleEndpoint},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := r.register(tt.impl.Descriptor().Path, tt.impl)
			if err != nil {
				t.Fatalf("register failed: %v", err)
			}

			entry, ok := r.Lookup(tt.impl.Descriptor().Path)
			if !ok {
				t.Fatal("decorator not found")
			}

			found := false
			for _, role := range entry.Roles {
				if role == tt.wantRole {
					found = true
					break
				}
			}

			if !found {
				t.Errorf("expected role %v, got %v", tt.wantRole, entry.Roles)
			}
		})
	}
}

// TestGlobalRegistration verifies database/sql pattern
func TestGlobalRegistration(t *testing.T) {
	// Simulate init() registration
	varDec := &mockValueDecorator{path: "test.var"}
	err := Register("test.var", varDec)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Lookup from global registry
	entry, ok := Global().Lookup("test.var")
	if !ok {
		t.Fatal("decorator not found in global registry")
	}

	if entry.Impl.Descriptor().Path != "test.var" {
		t.Errorf("path: got %q, want %q", entry.Impl.Descriptor().Path, "test.var")
	}
}

// TestURIBasedLookup verifies path-based lookup
func TestURIBasedLookup(t *testing.T) {
	r := NewRegistry()

	// Register hierarchical paths
	r.register("env", &mockValueDecorator{path: "env"})
	r.register("aws.secret", &mockValueDecorator{path: "aws.secret"})
	r.register("aws.s3.object", &mockMultiRoleDecorator{path: "aws.s3.object"})

	tests := []struct {
		path      string
		wantFound bool
	}{
		{"env", true},
		{"aws.secret", true},
		{"aws.s3.object", true},
		{"aws", false},    // Partial path doesn't match
		{"aws.s3", false}, // Partial path doesn't match
		{"nonexistent", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			_, found := r.Lookup(tt.path)
			if found != tt.wantFound {
				t.Errorf("Lookup(%q): found=%v, want=%v", tt.path, found, tt.wantFound)
			}
		})
	}
}

// TestPrimaryParamHandling verifies PrimaryParam is handled correctly
func TestPrimaryParamHandling(t *testing.T) {
	r := NewRegistry()

	// Register decorator that uses primary param
	envDec := &mockValueDecorator{path: "env"}
	r.register("env", envDec)

	// Lookup decorator by path (not including primary param)
	entry, ok := r.Lookup("env")
	if !ok {
		t.Fatal("decorator not found")
	}

	// Primary param is passed in ValueCall, not in path lookup
	primary := "HOME"
	call := ValueCall{
		Path:    "env",
		Primary: &primary,
		Params:  map[string]any{},
	}

	// Verify decorator receives primary param
	value, ok := entry.Impl.(Value)
	if !ok {
		t.Fatal("decorator doesn't implement Value")
	}

	results, err := value.Resolve(ValueEvalContext{}, call)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if results[0].Error != nil {
		t.Fatalf("result error: %v", results[0].Error)
	}

	// Mock returns "mock-value" regardless, but primary param was passed
	if results[0].Value != "mock-value" {
		t.Errorf("unexpected result: %v", results[0].Value)
	}
}

// TestValueCallConstruction verifies ValueCall is constructed correctly
func TestValueCallConstruction(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		primary     *string
		params      map[string]any
		wantPath    string
		wantPrimary *string
	}{
		{
			name:        "with primary param",
			path:        "env",
			primary:     strPtr("HOME"),
			params:      map[string]any{},
			wantPath:    "env",
			wantPrimary: strPtr("HOME"),
		},
		{
			name:        "without primary param",
			path:        "retry",
			primary:     nil,
			params:      map[string]any{"attempts": 3},
			wantPath:    "retry",
			wantPrimary: nil,
		},
		{
			name:        "hierarchical path with primary",
			path:        "aws.secret",
			primary:     strPtr("API_KEY"),
			params:      map[string]any{},
			wantPath:    "aws.secret",
			wantPrimary: strPtr("API_KEY"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			call := ValueCall{
				Path:    tt.path,
				Primary: tt.primary,
				Params:  tt.params,
			}

			if call.Path != tt.wantPath {
				t.Errorf("Path: got %q, want %q", call.Path, tt.wantPath)
			}

			if (call.Primary == nil) != (tt.wantPrimary == nil) {
				t.Errorf("Primary nil mismatch: got %v, want %v", call.Primary, tt.wantPrimary)
			}

			if call.Primary != nil && tt.wantPrimary != nil && *call.Primary != *tt.wantPrimary {
				t.Errorf("Primary: got %q, want %q", *call.Primary, *tt.wantPrimary)
			}
		})
	}
}

func TestRegistryGettersUnknownPath(t *testing.T) {
	r := NewRegistry()

	if _, ok, reason := r.GetValue("missing"); ok {
		t.Fatal("GetValue should return false for unknown path")
	} else if reason != "" {
		t.Fatalf("GetValue should not return reason for unknown path, got %q", reason)
	}
	if _, ok, reason := r.GetExec("missing"); ok {
		t.Fatal("GetExec should return false for unknown path")
	} else if reason != "" {
		t.Fatalf("GetExec should not return reason for unknown path, got %q", reason)
	}
	if _, ok, reason := r.GetTransport("missing"); ok {
		t.Fatal("GetTransport should return false for unknown path")
	} else if reason != "" {
		t.Fatalf("GetTransport should not return reason for unknown path, got %q", reason)
	}
	if _, ok, reason := r.GetRedirectTarget("missing"); ok {
		t.Fatal("GetRedirectTarget should return false for unknown path")
	} else if reason != "" {
		t.Fatalf("GetRedirectTarget should not return reason for unknown path, got %q", reason)
	}
}

func TestRegistryGettersByRole(t *testing.T) {
	r := NewRegistry()

	valueDec := &mockValueDecorator{path: "test.value"}
	execIODec := &mockExecIODecorator{path: "test.execio"}
	transportDec := &mockTransportDecorator{path: "test.transport"}

	if err := r.register(valueDec.path, valueDec); err != nil {
		t.Fatalf("register value decorator: %v", err)
	}
	if err := r.register(execIODec.path, execIODec); err != nil {
		t.Fatalf("register exec+io decorator: %v", err)
	}
	if err := r.register(transportDec.path, transportDec); err != nil {
		t.Fatalf("register transport decorator: %v", err)
	}

	if _, ok, reason := r.GetValue(valueDec.path); !ok {
		t.Fatal("GetValue should succeed for value decorator")
	} else if reason != "" {
		t.Fatalf("GetValue should not return reason on success, got %q", reason)
	}
	if _, ok, reason := r.GetTransport(valueDec.path); ok {
		t.Fatal("GetTransport should fail for value decorator")
	} else if diff := cmp.Diff("decorator \"test.value\" is not a transport", reason); diff != "" {
		t.Fatalf("GetTransport reason mismatch (-want +got):\n%s", diff)
	}

	if _, ok, reason := r.GetExec(execIODec.path); !ok {
		t.Fatal("GetExec should succeed for exec+io decorator")
	} else if reason != "" {
		t.Fatalf("GetExec should not return reason on success, got %q", reason)
	}
	if _, ok, reason := r.GetRedirectTarget(execIODec.path); !ok {
		t.Fatal("GetRedirectTarget should succeed for exec+io decorator")
	} else if reason != "" {
		t.Fatalf("GetRedirectTarget should not return reason on success, got %q", reason)
	}

	if _, ok, reason := r.GetTransport(transportDec.path); !ok {
		t.Fatal("GetTransport should succeed for transport decorator")
	} else if reason != "" {
		t.Fatalf("GetTransport should not return reason on success, got %q", reason)
	}
	if _, ok, reason := r.GetValue(transportDec.path); ok {
		t.Fatal("GetValue should fail for transport decorator")
	} else if diff := cmp.Diff("decorator \"test.transport\" does not implement Value interface", reason); diff != "" {
		t.Fatalf("GetValue reason mismatch (-want +got):\n%s", diff)
	}
}

// TestExport verifies Export returns all registered decorators
func TestExport(t *testing.T) {
	r := NewRegistry()

	// Register multiple decorators
	r.register("var", &mockValueDecorator{path: "var"})
	r.register("retry", &mockExecDecorator{path: "retry"})
	r.register("aws.s3.object", &mockMultiRoleDecorator{path: "aws.s3.object"})

	descriptors := r.Export()

	if len(descriptors) != 3 {
		t.Fatalf("expected 3 descriptors, got %d", len(descriptors))
	}

	// Verify roles are included in exported descriptors
	for _, desc := range descriptors {
		if len(desc.Roles) == 0 {
			t.Errorf("descriptor %q has no roles", desc.Path)
		}
	}
}

// TestConcurrentAccess verifies registry is thread-safe
func TestConcurrentAccess(t *testing.T) {
	r := NewRegistry()

	// Register initial decorator
	r.register("var", &mockValueDecorator{path: "var"})

	// Concurrent reads
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			_, ok := r.Lookup("var")
			if !ok {
				t.Error("concurrent lookup failed")
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}

// TestIsRegistered verifies IsRegistered method
func TestIsRegistered(t *testing.T) {
	r := NewRegistry()

	r.register("env", &mockValueDecorator{path: "env"})

	if !r.IsRegistered("env") {
		t.Error("IsRegistered(env) = false, want true")
	}

	if r.IsRegistered("nonexistent") {
		t.Error("IsRegistered(nonexistent) = true, want false")
	}
}

// Helper functions

func strPtr(s string) *string {
	return &s
}

// Mock decorators for testing

type mockValueDecorator struct {
	path               string
	transportSensitive bool
}

// Compile-time check that mockValueDecorator implements Value
var _ Value = (*mockValueDecorator)(nil)

func (m *mockValueDecorator) Descriptor() Descriptor {
	return Descriptor{
		Path: m.path,
		Capabilities: Capabilities{
			TransportSensitive: m.transportSensitive,
		},
	}
}

func (m *mockValueDecorator) Resolve(ctx ValueEvalContext, calls ...ValueCall) ([]ResolveResult, error) {
	results := make([]ResolveResult, len(calls))
	for i := range calls {
		results[i] = ResolveResult{
			Value:  "mock-value",
			Origin: "mock.value",
			Error:  nil,
		}
	}
	return results, nil
}

type mockExecDecorator struct {
	path string
}

func (m *mockExecDecorator) Descriptor() Descriptor {
	return Descriptor{Path: m.path}
}

func (m *mockExecDecorator) Wrap(next ExecNode, params map[string]any) ExecNode {
	return nil // Stub
}

type mockTransportDecorator struct {
	path string
}

func (m *mockTransportDecorator) Descriptor() Descriptor {
	return Descriptor{Path: m.path}
}

func (m *mockTransportDecorator) Capabilities() TransportCaps {
	return TransportCapNetwork
}

func (m *mockTransportDecorator) Open(parent Session, params map[string]any) (Session, error) {
	return nil, nil // Stub
}

func (m *mockTransportDecorator) Wrap(next ExecNode, params map[string]any) ExecNode {
	return nil // Stub
}

func (m *mockTransportDecorator) MaterializeSession() bool {
	return false
}

func (m *mockTransportDecorator) IsolationContext() IsolationContext {
	return nil
}

type mockIODecorator struct {
	path string
}

func (m *mockIODecorator) Descriptor() Descriptor {
	return Descriptor{Path: m.path}
}

func (m *mockIODecorator) IOCaps() IOCaps {
	return IOCaps{Read: true, Write: true, Append: true}
}

func (m *mockIODecorator) OpenRead(ctx ExecContext) (io.ReadCloser, error) {
	return nil, nil // Stub
}

func (m *mockIODecorator) OpenWrite(ctx ExecContext, appendMode bool) (Output, error) {
	return nil, nil // Stub
}

type mockMultiRoleDecorator struct {
	path string
}

type mockExecIODecorator struct {
	path string
}

func (m *mockExecIODecorator) Descriptor() Descriptor {
	return Descriptor{Path: m.path}
}

func (m *mockExecIODecorator) Wrap(next ExecNode, params map[string]any) ExecNode {
	return nil
}

func (m *mockExecIODecorator) IOCaps() IOCaps {
	return IOCaps{Read: false, Write: true, Append: true}
}

func (m *mockExecIODecorator) OpenRead(ctx ExecContext) (io.ReadCloser, error) {
	return nil, nil
}

func (m *mockExecIODecorator) OpenWrite(ctx ExecContext, appendMode bool) (Output, error) {
	return nil, nil
}

func (m *mockMultiRoleDecorator) Descriptor() Descriptor {
	return Descriptor{Path: m.path}
}

func (m *mockMultiRoleDecorator) Resolve(ctx ValueEvalContext, calls ...ValueCall) ([]ResolveResult, error) {
	results := make([]ResolveResult, len(calls))
	for i := range calls {
		results[i] = ResolveResult{
			Value:  map[string]any{"size": 1024},
			Origin: "mock.multi",
			Error:  nil,
		}
	}
	return results, nil
}

func (m *mockMultiRoleDecorator) IOCaps() IOCaps {
	return IOCaps{Read: true, Write: true, Append: true}
}

func (m *mockMultiRoleDecorator) OpenRead(ctx ExecContext) (io.ReadCloser, error) {
	return nil, nil // Stub
}

func (m *mockMultiRoleDecorator) OpenWrite(ctx ExecContext, appendMode bool) (Output, error) {
	return nil, nil // Stub
}

type mockSchemaValueDecorator struct {
	path string
}

func (m *mockSchemaValueDecorator) Descriptor() Descriptor {
	return NewDescriptor(m.path).
		Summary("schema-backed mock value decorator").
		PrimaryParamString("name", "Name value").
		Done().
		ParamString("default", "Fallback value").
		Default("fallback").
		Done().
		Build()
}

func (m *mockSchemaValueDecorator) Resolve(ctx ValueEvalContext, calls ...ValueCall) ([]ResolveResult, error) {
	results := make([]ResolveResult, len(calls))
	for i, call := range calls {
		name := ""
		if call.Primary != nil {
			name = *call.Primary
		}
		fallback, _ := call.Params["default"].(string)
		results[i] = ResolveResult{
			Value:  fmt.Sprintf("%s|%s", name, fallback),
			Origin: "mock.schema",
			Error:  nil,
		}
	}
	return results, nil
}

// Mock decorator with transport scope for testing
type mockScopedValueDecorator struct {
	path  string
	scope TransportScope
}

func (m *mockScopedValueDecorator) Descriptor() Descriptor {
	return Descriptor{
		Path: m.path,
		Capabilities: Capabilities{
			TransportScope: m.scope,
		},
	}
}

func (m *mockScopedValueDecorator) Resolve(ctx ValueEvalContext, calls ...ValueCall) ([]ResolveResult, error) {
	results := make([]ResolveResult, len(calls))
	for i := range calls {
		results[i] = ResolveResult{
			Value:  "scoped-value",
			Origin: "mock.scoped",
			Error:  nil,
		}
	}
	return results, nil
}

// TestTransportScopeAllows verifies TransportScope.Allows() logic
func TestTransportScopeAllows(t *testing.T) {
	tests := []struct {
		name     string
		scope    TransportScope
		current  TransportScope
		expected bool
	}{
		// TransportScopeAny allows everything
		{"Any allows Local", TransportScopeAny, TransportScopeLocal, true},
		{"Any allows SSH", TransportScopeAny, TransportScopeSSH, true},
		{"Any allows Remote", TransportScopeAny, TransportScopeRemote, true},
		{"Any allows Any", TransportScopeAny, TransportScopeAny, true},

		// TransportScopeLocal only allows Local
		{"Local allows Local", TransportScopeLocal, TransportScopeLocal, true},
		{"Local denies SSH", TransportScopeLocal, TransportScopeSSH, false},
		{"Local denies Remote", TransportScopeLocal, TransportScopeRemote, false},

		// TransportScopeSSH only allows SSH
		{"SSH allows SSH", TransportScopeSSH, TransportScopeSSH, true},
		{"SSH denies Local", TransportScopeSSH, TransportScopeLocal, false},

		// TransportScopeRemote allows any remote (SSH, Docker, etc.)
		{"Remote allows SSH", TransportScopeRemote, TransportScopeSSH, true},
		{"Remote denies Local", TransportScopeRemote, TransportScopeLocal, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.scope.Allows(tt.current)
			if result != tt.expected {
				t.Errorf("TransportScope(%v).Allows(%v) = %v, want %v",
					tt.scope, tt.current, result, tt.expected)
			}
		})
	}
}

// TestResolveValueSuccess verifies successful value resolution
func TestResolveValueSuccess(t *testing.T) {
	r := NewRegistry()

	// Register a value decorator
	dec := &mockValueDecorator{path: "var"}
	r.register("var", dec)

	// Create context
	session := NewLocalSession()
	ctx := ValueEvalContext{
		Session: session,
	}

	// Create call
	varName := "name"
	call := ValueCall{
		Path:    "var",
		Primary: &varName,
		Params:  map[string]any{},
	}

	// Resolve value
	resolved, err := r.ResolveValue(ctx, call, TransportScopeLocal)
	if err != nil {
		t.Fatalf("ResolveValue failed: %v", err)
	}

	// Verify result
	if resolved.Value != "mock-value" {
		t.Errorf("Value: got %v, want %q", resolved.Value, "mock-value")
	}
}

// TestResolveValueNotFound verifies error when decorator doesn't exist
func TestResolveValueNotFound(t *testing.T) {
	r := NewRegistry()

	session := NewLocalSession()
	ctx := ValueEvalContext{
		Session: session,
	}

	call := ValueCall{
		Path:    "nonexistent",
		Primary: nil,
		Params:  map[string]any{},
	}

	_, err := r.ResolveValue(ctx, call, TransportScopeLocal)
	if err == nil {
		t.Fatal("Expected error for nonexistent decorator")
	}

	expectedMsg := `decorator "nonexistent" not found`
	if err.Error() != expectedMsg {
		t.Errorf("Error message: got %q, want %q", err.Error(), expectedMsg)
	}
}

// TestResolveValueNotValueDecorator verifies error when decorator doesn't implement Value
func TestResolveValueNotValueDecorator(t *testing.T) {
	r := NewRegistry()

	// Register an Exec decorator (not Value)
	dec := &mockExecDecorator{path: "retry"}
	r.register("retry", dec)

	session := NewLocalSession()
	ctx := ValueEvalContext{
		Session: session,
	}

	call := ValueCall{
		Path:    "retry",
		Primary: nil,
		Params:  map[string]any{},
	}

	_, err := r.ResolveValue(ctx, call, TransportScopeLocal)
	if err == nil {
		t.Fatal("Expected error for non-value decorator")
	}

	expectedMsg := `decorator "retry" does not implement Value interface`
	if err.Error() != expectedMsg {
		t.Errorf("Error message: got %q, want %q", err.Error(), expectedMsg)
	}
}

// TestResolveValueScopeViolation verifies transport scope enforcement
func TestResolveValueScopeViolation(t *testing.T) {
	r := NewRegistry()

	// Register a Local-only decorator
	dec := &mockScopedValueDecorator{
		path:  "local.file",
		scope: TransportScopeLocal,
	}
	r.register("local.file", dec)

	session := NewLocalSession()
	ctx := ValueEvalContext{
		Session: session,
	}

	call := ValueCall{
		Path:    "local.file",
		Primary: nil,
		Params:  map[string]any{},
	}

	// Try to resolve in SSH context (should fail)
	_, err := r.ResolveValue(ctx, call, TransportScopeSSH)
	if err == nil {
		t.Fatal("Expected error for scope violation")
	}

	expectedMsg := `decorator "local.file" cannot be used in current transport scope (requires Local, current: SSH)`
	if err.Error() != expectedMsg {
		t.Errorf("Error message: got %q, want %q", err.Error(), expectedMsg)
	}
}

// TestResolveValueScopeAllowed verifies scope enforcement allows valid scopes
func TestResolveValueScopeAllowed(t *testing.T) {
	r := NewRegistry()

	// Register an Any-scope decorator
	dec := &mockScopedValueDecorator{
		path:  "env",
		scope: TransportScopeAny,
	}
	r.register("env", dec)

	session := NewLocalSession()
	ctx := ValueEvalContext{
		Session: session,
	}

	call := ValueCall{
		Path:    "env",
		Primary: nil,
		Params:  map[string]any{},
	}

	// Should work in Local context
	_, err := r.ResolveValue(ctx, call, TransportScopeLocal)
	if err != nil {
		t.Errorf("ResolveValue in Local failed: %v", err)
	}

	// Should work in SSH context
	_, err = r.ResolveValue(ctx, call, TransportScopeSSH)
	if err != nil {
		t.Errorf("ResolveValue in SSH failed: %v", err)
	}
}

func TestResolveValueNormalizesPrimaryAndAppliesDefaults(t *testing.T) {
	r := NewRegistry()

	dec := &mockSchemaValueDecorator{path: "strict"}
	r.register("strict", dec)

	ctx := ValueEvalContext{Session: NewLocalSession()}
	call := ValueCall{
		Path:   "strict",
		Params: map[string]any{"arg1": "HOME"},
	}

	resolved, err := r.ResolveValue(ctx, call, TransportScopeLocal)
	if err != nil {
		t.Fatalf("ResolveValue failed: %v", err)
	}

	if resolved.Value != "HOME|fallback" {
		t.Errorf("Value: got %v, want %q", resolved.Value, "HOME|fallback")
	}
}

func TestResolveValueRejectsUnknownParameter(t *testing.T) {
	r := NewRegistry()

	dec := &mockSchemaValueDecorator{path: "strict"}
	r.register("strict", dec)

	ctx := ValueEvalContext{Session: NewLocalSession()}
	call := ValueCall{
		Path:   "strict",
		Params: map[string]any{"arg1": "HOME", "unknown": "value"},
	}

	_, err := r.ResolveValue(ctx, call, TransportScopeLocal)
	if err == nil {
		t.Fatal("expected unknown parameter error")
	}

	expectedMsg := `invalid parameters for decorator "strict" call 0: unknown parameter "unknown"`
	if err.Error() != expectedMsg {
		t.Errorf("Error message: got %q, want %q", err.Error(), expectedMsg)
	}
}

func TestResolveValueRejectsPrimaryTypeMismatch(t *testing.T) {
	r := NewRegistry()

	dec := &mockSchemaValueDecorator{path: "strict"}
	r.register("strict", dec)

	ctx := ValueEvalContext{Session: NewLocalSession()}
	call := ValueCall{
		Path:   "strict",
		Params: map[string]any{"arg1": 123},
	}

	_, err := r.ResolveValue(ctx, call, TransportScopeLocal)
	if err == nil {
		t.Fatal("expected primary type mismatch error")
	}

	expectedMsg := `invalid parameters for decorator "strict" call 0: parameter "name" expects string`
	if err.Error() != expectedMsg {
		t.Errorf("Error message: got %q, want %q", err.Error(), expectedMsg)
	}
}

// ========== TransportSensitive Capability Tests ==========

// TestTransportSensitive_BuilderDefaultIsFalse verifies TransportSensitive defaults to false in builder
func TestTransportSensitive_BuilderDefaultIsFalse(t *testing.T) {
	desc := NewDescriptor("test").
		Summary("Test decorator").
		Build()

	if desc.Capabilities.TransportSensitive {
		t.Error("TransportSensitive should default to false in builder")
	}
}

// TestTransportSensitive_BuilderCanBeSetTrue verifies TransportSensitive can be set to true in builder
func TestTransportSensitive_BuilderCanBeSetTrue(t *testing.T) {
	desc := NewDescriptor("test").
		Summary("Test decorator").
		TransportSensitive().
		Build()

	if !desc.Capabilities.TransportSensitive {
		t.Error("TransportSensitive should be true after calling TransportSensitive()")
	}
}

// TestTransportSensitive_EnvDecoratorIsTransportSensitive verifies the real @env decorator
// is registered with TransportSensitive=true.
//
// NOTE: This test requires the runtime/decorators package to be imported by the test binary.
// In core/decorator tests, we can't import runtime (circular dependency), so we use a mock.
func TestTransportSensitive_EnvDecoratorIsTransportSensitive(t *testing.T) {
	// Register a mock @env decorator with the expected capability
	registry := NewRegistry()
	mockEnv := &mockValueDecorator{
		path:               "env",
		transportSensitive: true,
	}
	if err := registry.register("env", mockEnv); err != nil {
		t.Fatalf("Failed to register mock @env: %v", err)
	}

	entry, ok := registry.Lookup("env")
	if !ok {
		t.Fatal("@env not found in registry")
	}

	desc := entry.Impl.Descriptor()
	if !desc.Capabilities.TransportSensitive {
		t.Error("@env decorator should be transport-sensitive")
	}
}

// TestTransportSensitive_VarDecoratorIsNotTransportSensitive verifies the real @var decorator
// is registered with TransportSensitive=false.
//
// NOTE: This test requires the runtime/decorators package to be imported by the test binary.
// In core/decorator tests, we can't import runtime (circular dependency), so we use a mock.
func TestTransportSensitive_VarDecoratorIsNotTransportSensitive(t *testing.T) {
	// Register a mock @var decorator with the expected capability
	registry := NewRegistry()
	mockVar := &mockValueDecorator{
		path:               "var",
		transportSensitive: false,
	}
	if err := registry.register("var", mockVar); err != nil {
		t.Fatalf("Failed to register mock @var: %v", err)
	}

	entry, ok := registry.Lookup("var")
	if !ok {
		t.Fatal("@var not found in registry")
	}

	desc := entry.Impl.Descriptor()
	if desc.Capabilities.TransportSensitive {
		t.Error("@var decorator should NOT be transport-sensitive")
	}
}
