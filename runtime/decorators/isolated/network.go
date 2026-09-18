package isolated

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/builtwithtofu/sigil/core/decorator"
	"github.com/builtwithtofu/sigil/runtime/isolation"
)

type NetworkLoopbackDecorator struct{}

var _ decorator.Transport = (*NetworkLoopbackDecorator)(nil)

func (d *NetworkLoopbackDecorator) Descriptor() decorator.Descriptor {
	desc := decorator.NewDescriptor(FullPath("network.loopback")).
		Summary("Execute in isolation with loopback-only networking").
		Roles(decorator.RoleBoundary).
		Block(decorator.BlockRequired).
		Build()

	desc.Capabilities.SupportedOS = []string{"linux"}

	return desc
}

func (d *NetworkLoopbackDecorator) Capabilities() decorator.TransportCaps {
	return decorator.TransportCapNetwork |
		decorator.TransportCapFilesystem |
		decorator.TransportCapEnvironment |
		decorator.TransportCapIsolation
}

func (d *NetworkLoopbackDecorator) Open(parent decorator.Session, params map[string]any) (decorator.Session, error) {
	_ = params

	isolator := isolation.NewIsolator()
	config := decorator.IsolationConfig{
		NetworkPolicy:    decorator.NetworkPolicyLoopbackOnly,
		FilesystemPolicy: decorator.FilesystemPolicyFull,
		MemoryLock:       false,
	}

	if err := isolator.Isolate(decorator.IsolationLevelStandard, config); err != nil {
		return nil, fmt.Errorf("failed to create loopback-only isolation: %w", err)
	}

	return &networkLoopbackSession{parent: parent, isolator: isolator}, nil
}

func (d *NetworkLoopbackDecorator) Wrap(next decorator.ExecNode, params map[string]any) decorator.ExecNode {
	_ = params
	return next
}

func (d *NetworkLoopbackDecorator) MaterializeSession() bool {
	return true
}

func (d *NetworkLoopbackDecorator) IsolationContext() decorator.IsolationContext {
	return isolation.NewIsolator()
}

type networkLoopbackSession struct {
	parent   decorator.Session
	isolator decorator.IsolationContext
}

var _ decorator.Session = (*networkLoopbackSession)(nil)

func (s *networkLoopbackSession) Run(ctx context.Context, argv []string, opts decorator.RunOpts) (decorator.Result, error) {
	return s.parent.Run(ctx, argv, opts)
}

func (s *networkLoopbackSession) OpenFileOutput(ctx context.Context, path string, mode decorator.FileWriteMode, perm fs.FileMode) (decorator.Output, error) {
	return decorator.OpenFileOutput(ctx, s.parent, path, mode, perm)
}

func (s *networkLoopbackSession) Put(ctx context.Context, data []byte, path string, mode fs.FileMode) error {
	return s.parent.Put(ctx, data, path, mode)
}

func (s *networkLoopbackSession) Get(ctx context.Context, path string) ([]byte, error) {
	return s.parent.Get(ctx, path)
}

func (s *networkLoopbackSession) Env() map[string]string {
	return s.parent.Env()
}

func (s *networkLoopbackSession) WithEnv(delta map[string]string) decorator.Session {
	return &networkLoopbackSession{parent: s.parent.WithEnv(delta), isolator: s.isolator}
}

func (s *networkLoopbackSession) WithWorkdir(dir string) decorator.Session {
	return &networkLoopbackSession{parent: s.parent.WithWorkdir(dir), isolator: s.isolator}
}

func (s *networkLoopbackSession) Cwd() string {
	return s.parent.Cwd()
}

func (s *networkLoopbackSession) Platform() string {
	return s.parent.Platform()
}

func (s *networkLoopbackSession) ID() string {
	return s.parent.ID() + "/isolated.network.loopback"
}

func (s *networkLoopbackSession) TransportScope() decorator.TransportScope {
	return decorator.TransportScopeIsolated
}

func (s *networkLoopbackSession) Close() error {
	return nil
}

func init() {
	path := FullPath("network.loopback")
	if err := decorator.Register(path, &NetworkLoopbackDecorator{}); err != nil {
		panic(fmt.Sprintf("failed to register @%s decorator: %v", path, err))
	}
}
