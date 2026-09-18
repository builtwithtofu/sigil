package executor

import (
	"context"
	"io/fs"

	"github.com/builtwithtofu/sigil/core/decorator"
)

type transportScopedSession struct {
	id      string
	session decorator.Session
}

func (s *transportScopedSession) Run(ctx context.Context, argv []string, opts decorator.RunOpts) (decorator.Result, error) {
	return s.session.Run(ctx, argv, opts)
}

func (s *transportScopedSession) Put(ctx context.Context, data []byte, path string, mode fs.FileMode) error {
	return s.session.Put(ctx, data, path, mode)
}

func (s *transportScopedSession) Get(ctx context.Context, path string) ([]byte, error) {
	return s.session.Get(ctx, path)
}

func (s *transportScopedSession) Env() map[string]string {
	return s.session.Env()
}

func (s *transportScopedSession) WithEnv(delta map[string]string) decorator.Session {
	return &transportScopedSession{id: s.id, session: s.session.WithEnv(delta)}
}

func (s *transportScopedSession) WithWorkdir(dir string) decorator.Session {
	return &transportScopedSession{id: s.id, session: s.session.WithWorkdir(dir)}
}

func (s *transportScopedSession) Cwd() string {
	return s.session.Cwd()
}

func (s *transportScopedSession) Platform() string {
	return s.session.Platform()
}

func (s *transportScopedSession) ID() string {
	return s.id
}

func (s *transportScopedSession) TransportScope() decorator.TransportScope {
	return s.session.TransportScope()
}

func (s *transportScopedSession) Close() error {
	return s.session.Close()
}

func (s *transportScopedSession) UnwrapSession() decorator.Session {
	return s.session
}

func (s *transportScopedSession) OpenFileOutput(ctx context.Context, path string, mode decorator.FileWriteMode, perm fs.FileMode) (decorator.Output, error) {
	return decorator.OpenFileOutput(ctx, s.session, path, mode, perm)
}
