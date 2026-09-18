package decorators

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"

	"github.com/builtwithtofu/sigil/core/decorator"
)

// FileSinkDecorator provides file endpoints in the selected execution session.
type FileSinkDecorator struct{ params map[string]any }

type fileConfig struct {
	Path   string `decorator:"path"`
	Perm   int64  `decorator:"perm"`
	Atomic bool   `decorator:"atomic"`
}

func (d *FileSinkDecorator) Descriptor() decorator.Descriptor {
	return decorator.NewDescriptor("file").
		Summary("File endpoint for streaming redirection").
		ParamString("path", "File path in the execution session").Required().Done().
		ParamInt("perm", "Creation permissions").Default(int64(0o644)).Min(0).Max(0o777).Done().
		ParamBool("atomic", "Publish replacement only after successful production").Default(false).Done().
		Block(decorator.BlockForbidden).TransportScope(decorator.TransportScopeAny).
		Roles(decorator.RoleEndpoint).Build()
}

func (d *FileSinkDecorator) IOCaps() decorator.IOCaps {
	return decorator.IOCaps{Read: true, Write: true, Append: true}
}

func (d *FileSinkDecorator) config() (fileConfig, error) {
	params := make(map[string]any, len(d.params))
	for key, value := range d.params {
		params[key] = value
	}
	if perm, ok := params["perm"].(int); ok {
		params["perm"] = int64(perm)
	}
	cfg, _, err := decorator.DecodeInto[fileConfig](d.Descriptor().Schema, nil, params)
	if err != nil {
		return cfg, err
	}
	if cfg.Path == "" {
		return cfg, fmt.Errorf("@file requires path parameter")
	}
	return cfg, nil
}

func (d *FileSinkDecorator) OpenRead(ctx decorator.ExecContext) (io.ReadCloser, error) {
	cfg, err := d.config()
	if err != nil {
		return nil, err
	}
	if ctx.Session == nil {
		return nil, fmt.Errorf("execution context missing session")
	}
	c := ctx.Context
	if c == nil {
		c = context.Background()
	}
	data, err := ctx.Session.Get(c, resolvePath(cfg.Path, ctx.Session))
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (d *FileSinkDecorator) OpenWrite(ctx decorator.ExecContext, appendMode bool) (decorator.Output, error) {
	cfg, err := d.config()
	if err != nil {
		return nil, err
	}
	c := ctx.Context
	if c == nil {
		c = context.Background()
	}
	mode := decorator.FileTruncate
	if appendMode {
		mode = decorator.FileAppend
	}
	if cfg.Atomic {
		if appendMode {
			return nil, fmt.Errorf("atomic append is not supported; use > for replacement")
		}
		mode = decorator.FileReplaceOnSuccess
	}
	output, err := decorator.OpenFileOutput(c, ctx.Session, cfg.Path, mode, fs.FileMode(cfg.Perm))
	if err != nil {
		return nil, err
	}
	return output, nil
}

func resolvePath(p string, session decorator.Session) string {
	if session.Platform() == "windows" {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(session.Cwd(), p)
	}
	if path.IsAbs(p) {
		return p
	}
	return path.Join(session.Cwd(), p)
}

func (d *FileSinkDecorator) WithParams(params map[string]any) decorator.IO {
	return &FileSinkDecorator{params: params}
}

func init() {
	if err := decorator.Register("file", &FileSinkDecorator{}); err != nil {
		panic(err)
	}
}
