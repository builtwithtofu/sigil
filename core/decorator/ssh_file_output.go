package decorator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sync"

	"golang.org/x/crypto/ssh"
)

func (s *SSHSession) OpenFileOutput(ctx context.Context, path string, mode FileWriteMode, perm fs.FileMode) (Output, error) {
	return s.openFileOutput(ctx, "", path, mode, perm)
}
func (s *SSHSessionWithEnv) OpenFileOutput(ctx context.Context, path string, mode FileWriteMode, perm fs.FileMode) (Output, error) {
	return s.base.openFileOutput(ctx, s.cwd, path, mode, perm)
}
func (s *SSHSession) openFileOutput(ctx context.Context, cwd, path string, mode FileWriteMode, perm fs.FileMode) (Output, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, errors.New("file path is empty")
	}
	if s.platform == "windows" {
		return nil, errors.New("SSH file output on Windows is not supported")
	}
	if perm.Perm()&0o111 != 0 {
		return nil, errors.New("executable creation permissions are not supported by SSH streaming output")
	}
	op := ">"
	switch mode {
	case FileTruncate:
	case FileAppend:
		op = ">>"
	default:
		return nil, errors.New("atomic replacement is not supported by SSH file output")
	}
	session, err := s.client.NewSession()
	if err != nil {
		return nil, err
	}
	fail := func(err error) (Output, error) { _ = session.Close(); return nil, err }
	stdin, err := session.StdinPipe()
	if err != nil {
		return fail(err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return fail(err)
	}
	w := &sshFileOutput{session: session, stdin: stdin, ctx: ctx}
	session.Stderr = &w.stderr
	command := fmt.Sprintf("umask %03o; exec 3%s%s || exit; printf 'READY\\n'; cat >&3", 0o777&^perm.Perm(), op, shellQuote(path))
	if cwd != "" {
		command = "cd " + shellQuote(cwd) + " && { " + command + "; }"
	}
	w.stop = context.AfterFunc(ctx, func() { _ = session.Close() })
	if err := session.Start(command); err != nil {
		w.stop()
		return fail(err)
	}
	// The acknowledgement is sent only after the destination descriptor is open.
	ready := make([]byte, 6)
	if _, err := io.ReadFull(stdout, ready); err != nil || string(ready) != "READY\n" {
		_ = stdin.Close()
		waitErr := session.Wait()
		w.stop()
		return fail(errors.Join(err, waitErr, ctx.Err(), fmt.Errorf("remote destination open failed: %s", w.stderr.String())))
	}
	return w, nil
}

type sshFileOutput struct {
	session *ssh.Session
	stdin   io.WriteCloser
	ctx     context.Context
	stop    func() bool
	stderr  bytes.Buffer
	once    sync.Once
	err     error
}

func (w *sshFileOutput) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.stdin.Write(p)
}
func (w *sshFileOutput) Finish(ctx context.Context) error {
	w.once.Do(func() {
		stop := context.AfterFunc(ctx, func() { _ = w.session.Close() })
		defer stop()
		closeErr := w.stdin.Close()
		waitErr := w.session.Wait()
		w.stop()
		_ = w.session.Close()
		w.err = errors.Join(closeErr, waitErr, w.ctx.Err(), ctx.Err())
		var exitErr *ssh.ExitError
		if waitErr != nil && !errors.As(waitErr, &exitErr) {
			w.err = errors.Join(w.err, ErrPublicationUnknown)
		}
		if waitErr != nil && w.stderr.Len() != 0 {
			w.err = fmt.Errorf("remote file write: %s: %w", w.stderr.String(), w.err)
		}
	})
	return w.err
}
func (w *sshFileOutput) Abort(ctx context.Context) error { return w.Finish(ctx) }
