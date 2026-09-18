package decorator

import (
	"context"
	"io/fs"
	"sync"
)

// MonitoredSession wraps a Session to track method calls for testing.
// This is a test helper that doesn't change behavior, just observes it.
type MonitoredSession struct {
	wrapped Session
	stats   *SessionStats
}

// SessionStats tracks session method calls for testing.
type SessionStats struct {
	mu sync.Mutex

	RunCalls     int
	PutCalls     int
	GetCalls     int
	EnvCalls     int
	WithEnvCalls int
	CwdCalls     int
	CloseCalls   int

	// Track WithEnv deltas to verify isolation
	WithEnvDeltas []map[string]string
}

// NewMonitoredSession wraps a session with monitoring.
func NewMonitoredSession(session Session) *MonitoredSession {
	return &MonitoredSession{
		wrapped: session,
		stats:   &SessionStats{},
	}
}

// Stats returns the accumulated statistics.
func (m *MonitoredSession) Stats() *SessionStats {
	return m.stats
}

// Session interface implementation (delegates to wrapped session)

func (m *MonitoredSession) Run(ctx context.Context, argv []string, opts RunOpts) (Result, error) {
	m.stats.mu.Lock()
	m.stats.RunCalls++
	m.stats.mu.Unlock()
	return m.wrapped.Run(ctx, argv, opts)
}

func (m *MonitoredSession) Put(ctx context.Context, data []byte, path string, mode fs.FileMode) error {
	m.stats.mu.Lock()
	m.stats.PutCalls++
	m.stats.mu.Unlock()
	return m.wrapped.Put(ctx, data, path, mode)
}

func (m *MonitoredSession) Get(ctx context.Context, path string) ([]byte, error) {
	m.stats.mu.Lock()
	m.stats.GetCalls++
	m.stats.mu.Unlock()
	return m.wrapped.Get(ctx, path)
}

func (m *MonitoredSession) Env() map[string]string {
	m.stats.mu.Lock()
	m.stats.EnvCalls++
	m.stats.mu.Unlock()
	return m.wrapped.Env()
}

func (m *MonitoredSession) WithEnv(delta map[string]string) Session {
	m.stats.mu.Lock()
	m.stats.WithEnvCalls++
	// Deep copy delta for inspection
	deltaCopy := make(map[string]string, len(delta))
	for k, v := range delta {
		deltaCopy[k] = v
	}
	m.stats.WithEnvDeltas = append(m.stats.WithEnvDeltas, deltaCopy)
	m.stats.mu.Unlock()

	// Wrap the returned session too
	newSession := m.wrapped.WithEnv(delta)
	return &MonitoredSession{
		wrapped: newSession,
		stats:   m.stats, // Share stats with parent
	}
}

func (m *MonitoredSession) WithWorkdir(dir string) Session {
	// Don't track WithWorkdir calls for now
	newSession := m.wrapped.WithWorkdir(dir)
	return &MonitoredSession{
		wrapped: newSession,
		stats:   m.stats, // Share stats with parent
	}
}

func (m *MonitoredSession) Cwd() string {
	m.stats.mu.Lock()
	m.stats.CwdCalls++
	m.stats.mu.Unlock()
	return m.wrapped.Cwd()
}

// ID returns the session identifier, delegating to the wrapped session.
func (m *MonitoredSession) ID() string {
	return m.wrapped.ID()
}

// TransportScope returns the transport scope, delegating to the wrapped session.
func (m *MonitoredSession) TransportScope() TransportScope {
	return m.wrapped.TransportScope()
}

func (m *MonitoredSession) Platform() string {
	return ""
}

func (m *MonitoredSession) Close() error {
	m.stats.mu.Lock()
	m.stats.CloseCalls++
	m.stats.mu.Unlock()
	return m.wrapped.Close()
}

// MonitoredTransport wraps a Transport to track Open calls.
type MonitoredTransport struct {
	wrapped   Transport
	OpenCalls int
	mu        sync.Mutex
}

// NewMonitoredTransport wraps a transport with monitoring.
func NewMonitoredTransport(transport Transport) *MonitoredTransport {
	return &MonitoredTransport{
		wrapped: transport,
	}
}

func (m *MonitoredTransport) Descriptor() Descriptor {
	return m.wrapped.Descriptor()
}

func (m *MonitoredTransport) Capabilities() TransportCaps {
	return m.wrapped.Capabilities()
}

func (m *MonitoredTransport) Open(parent Session, params map[string]any) (Session, error) {
	m.mu.Lock()
	m.OpenCalls++
	m.mu.Unlock()

	session, err := m.wrapped.Open(parent, params)
	if err != nil {
		return nil, err
	}

	// Wrap returned session for monitoring
	return NewMonitoredSession(session), nil
}

func (m *MonitoredTransport) Wrap(next ExecNode, params map[string]any) ExecNode {
	return m.wrapped.Wrap(next, params)
}

func (m *MonitoredTransport) MaterializeSession() bool {
	return m.wrapped.MaterializeSession()
}

func (m *MonitoredTransport) IsolationContext() IsolationContext {
	return m.wrapped.IsolationContext()
}

// ========== TestTransport ==========

// TestTransport is a mock transport decorator for testing transport boundaries.
// It creates a transport boundary without actually connecting to anything.
// The transport scope is derived from the name prefix (ssh:, docker:, etc.).
type TestTransport struct {
	name string // e.g., "ssh:test-server", "docker:container1"
}

// NewTestTransport creates a new test transport with the given name.
// The name determines the transport scope:
//   - "ssh:*" → TransportScopeSSH
//   - "docker:*", "k8s:*" → TransportScopeRemote
//   - "local" → TransportScopeLocal
//   - anything else → TransportScopeRemote
func NewTestTransport(name string) *TestTransport {
	return &TestTransport{name: name}
}

// Descriptor returns the decorator metadata.
func (t *TestTransport) Descriptor() Descriptor {
	return NewDescriptor("test.transport").
		Summary("Test transport for boundary testing").
		Roles(RoleBoundary).
		Block(BlockRequired).
		Build()
}

func (t *TestTransport) Capabilities() TransportCaps {
	return TransportCapNetwork | TransportCapFilesystem | TransportCapEnvironment
}

// Open creates a new session with the transport's scope.
// The session delegates all operations to the parent but reports
// a different TransportScope and ID.
func (t *TestTransport) Open(parent Session, params map[string]any) (Session, error) {
	return &testTransportSession{
		parent: parent,
		name:   t.name,
		scope:  t.deriveScope(),
	}, nil
}

// Wrap implements the Exec interface for transport decorators.
// For TestTransport, this is a no-op that just passes through.
func (t *TestTransport) Wrap(next ExecNode, params map[string]any) ExecNode {
	return next
}

func (t *TestTransport) MaterializeSession() bool {
	return true
}

func (t *TestTransport) IsolationContext() IsolationContext {
	return nil
}

// deriveScope determines the TransportScope from the transport name.
func (t *TestTransport) deriveScope() TransportScope {
	switch {
	case t.name == "local":
		return TransportScopeLocal
	case len(t.name) >= 4 && t.name[:4] == "ssh:":
		return TransportScopeSSH
	default:
		// docker:, k8s:, or anything else is remote
		return TransportScopeRemote
	}
}

// testTransportSession wraps a parent session with a different transport scope.
// All operations delegate to the parent, but TransportScope() and ID() return
// the transport's values.
type testTransportSession struct {
	parent Session
	name   string
	scope  TransportScope
}

func (s *testTransportSession) Run(ctx context.Context, argv []string, opts RunOpts) (Result, error) {
	return s.parent.Run(ctx, argv, opts)
}

func (s *testTransportSession) Put(ctx context.Context, data []byte, path string, mode fs.FileMode) error {
	return s.parent.Put(ctx, data, path, mode)
}

func (s *testTransportSession) Get(ctx context.Context, path string) ([]byte, error) {
	return s.parent.Get(ctx, path)
}

func (s *testTransportSession) Env() map[string]string {
	return s.parent.Env()
}

func (s *testTransportSession) WithEnv(delta map[string]string) Session {
	return &testTransportSession{
		parent: s.parent.WithEnv(delta),
		name:   s.name,
		scope:  s.scope,
	}
}

func (s *testTransportSession) WithWorkdir(dir string) Session {
	return &testTransportSession{
		parent: s.parent.WithWorkdir(dir),
		name:   s.name,
		scope:  s.scope,
	}
}

func (s *testTransportSession) Cwd() string {
	return s.parent.Cwd()
}

func (s *testTransportSession) ID() string {
	return s.name
}

func (s *testTransportSession) TransportScope() TransportScope {
	return s.scope
}

func (s *testTransportSession) Platform() string {
	return ""
}

func (s *testTransportSession) Close() error {
	// Don't close parent - we don't own it
	return nil
}

func (s *testTransportSession) OpenFileOutput(ctx context.Context, path string, mode FileWriteMode, perm fs.FileMode) (Output, error) {
	return OpenFileOutput(ctx, s.parent, path, mode, perm)
}

func (m *MonitoredSession) OpenFileOutput(ctx context.Context, path string, mode FileWriteMode, perm fs.FileMode) (Output, error) {
	return OpenFileOutput(ctx, m.wrapped, path, mode, perm)
}
