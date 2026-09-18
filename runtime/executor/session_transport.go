package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"

	"github.com/builtwithtofu/sigil/core/decorator"
	sdkexec "github.com/builtwithtofu/sigil/core/sdk/executor"
)

type sessionTransport struct {
	session decorator.Session
}

func newSessionTransport(session decorator.Session) *sessionTransport {
	return &sessionTransport{session: session}
}

func (t *sessionTransport) Exec(ctx context.Context, argv []string, opts sdkexec.ExecOpts) (int, error) {
	runSession := t.session
	if opts.Dir != "" {
		runSession = runSession.WithWorkdir(opts.Dir)
	}
	if len(opts.Env) > 0 {
		runSession = runSession.WithEnv(opts.Env)
	}

	result, err := runSession.Run(ctx, argv, decorator.RunOpts{
		Stdin:  opts.Stdin,
		Stdout: opts.Stdout,
		Stderr: opts.Stderr,
	})
	return result.ExitCode, err
}

func (t *sessionTransport) Put(ctx context.Context, src io.Reader, dst string, mode fs.FileMode) error {
	data, err := io.ReadAll(src)
	if err != nil {
		return err
	}

	return t.session.Put(ctx, data, dst, mode)
}

func (t *sessionTransport) Get(ctx context.Context, src string, dst io.Writer) error {
	data, err := t.session.Get(ctx, src)
	if err != nil {
		return err
	}

	_, err = dst.Write(data)
	return err
}

func (t *sessionTransport) OpenFileWriter(ctx context.Context, path string, mode sdkexec.RedirectMode, perm fs.FileMode) (io.WriteCloser, error) {
	writeMode := decorator.FileTruncate
	switch mode {
	case sdkexec.RedirectOverwrite:
	case sdkexec.RedirectAppend:
		writeMode = decorator.FileAppend
	default:
		return nil, errors.New("invalid file output mode")
	}
	output, err := decorator.OpenFileOutput(ctx, t.session, path, writeMode, perm)
	if err != nil {
		return nil, err
	}
	return decorator.CloseableOutput(ctx, output), nil
}

func (t *sessionTransport) OpenFileReader(ctx context.Context, path string) (io.ReadCloser, error) {
	data, err := t.session.Get(ctx, path)
	if err != nil {
		return nil, err
	}

	return io.NopCloser(bytes.NewReader(data)), nil
}

func (t *sessionTransport) Close() error {
	return nil
}
