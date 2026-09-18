package isolated

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/builtwithtofu/sigil/core/decorator"
	"github.com/builtwithtofu/sigil/runtime/isolation"
)

type MemoryLockDecorator struct{}

var _ decorator.Transport = (*MemoryLockDecorator)(nil)

func (d *MemoryLockDecorator) Descriptor() decorator.Descriptor {
	desc := decorator.NewDescriptor(FullPath("memory.lock")).
		Summary("Execute in isolation with memory locking enabled").
		Roles(decorator.RoleBoundary).
		Block(decorator.BlockRequired).
		Build()

	desc.Capabilities.SupportedOS = []string{"linux", "darwin"}

	return desc
}

func (d *MemoryLockDecorator) Capabilities() decorator.TransportCaps {
	return decorator.TransportCapNetwork |
		decorator.TransportCapFilesystem |
		decorator.TransportCapEnvironment |
		decorator.TransportCapIsolation
}

func (d *MemoryLockDecorator) Open(parent decorator.Session, params map[string]any) (decorator.Session, error) {
	_ = params

	isolator := isolation.NewIsolator()
	config := decorator.IsolationConfig{
		NetworkPolicy:    decorator.NetworkPolicyAllow,
		FilesystemPolicy: decorator.FilesystemPolicyFull,
		MemoryLock:       true,
	}

	if err := isolator.Isolate(decorator.IsolationLevelStandard, config); err != nil {
		return nil, fmt.Errorf("failed to create memory-lock isolation: %w", err)
	}

	return &memoryLockSession{parent: parent, isolator: isolator}, nil
}

func (d *MemoryLockDecorator) Wrap(next decorator.ExecNode, params map[string]any) decorator.ExecNode {
	_ = params
	return next
}

func (d *MemoryLockDecorator) MaterializeSession() bool {
	return true
}

func (d *MemoryLockDecorator) IsolationContext() decorator.IsolationContext {
	return isolation.NewIsolator()
}

type memoryLockSession struct {
	parent   decorator.Session
	isolator decorator.IsolationContext
}

var _ decorator.Session = (*memoryLockSession)(nil)

func (s *memoryLockSession) Run(ctx context.Context, argv []string, opts decorator.RunOpts) (decorator.Result, error) {
	return s.parent.Run(ctx, argv, opts)
}

func (s *memoryLockSession) Put(ctx context.Context, data []byte, path string, mode fs.FileMode) error {
	return s.parent.Put(ctx, data, path, mode)
}

func (s *memoryLockSession) OpenFileOutput(ctx context.Context, path string, mode decorator.FileWriteMode, perm fs.FileMode) (decorator.Output, error) {
	return decorator.OpenFileOutput(ctx, s.parent, path, mode, perm)
}

func (s *memoryLockSession) Get(ctx context.Context, path string) ([]byte, error) {
	return s.parent.Get(ctx, path)
}

func (s *memoryLockSession) Env() map[string]string {
	return s.parent.Env()
}

func (s *memoryLockSession) WithEnv(delta map[string]string) decorator.Session {
	return &memoryLockSession{parent: s.parent.WithEnv(delta), isolator: s.isolator}
}

func (s *memoryLockSession) WithWorkdir(dir string) decorator.Session {
	return &memoryLockSession{parent: s.parent.WithWorkdir(dir), isolator: s.isolator}
}

func (s *memoryLockSession) Cwd() string {
	return s.parent.Cwd()
}

func (s *memoryLockSession) Platform() string {
	return s.parent.Platform()
}

func (s *memoryLockSession) ID() string {
	return s.parent.ID() + "/isolated.memory.lock"
}

func (s *memoryLockSession) TransportScope() decorator.TransportScope {
	return decorator.TransportScopeIsolated
}

func (s *memoryLockSession) Close() error {
	return nil
}

func init() {
	path := FullPath("memory.lock")
	if err := decorator.Register(path, &MemoryLockDecorator{}); err != nil {
		panic(fmt.Sprintf("failed to register @%s decorator: %v", path, err))
	}
}
