package executor

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/builtwithtofu/sigil/core/decorator"
	"github.com/builtwithtofu/sigil/core/planfmt"
	_ "github.com/builtwithtofu/sigil/runtime/decorators"
	"github.com/google/go-cmp/cmp"
)

func TestFileSinkPathStyles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		decorator         string
		argKey            string
		path              string
		expectedStorePath func(string) string
	}{
		{
			name:      "file decorator accepts forward slash path",
			decorator: "@file",
			argKey:    "path",
			path:      "io/forward.txt",
			expectedStorePath: func(workdir string) string {
				return filepath.Join(workdir, "io", "forward.txt")
			},
		},
		{
			name:      "file decorator accepts backslash path",
			decorator: "@file",
			argKey:    "path",
			path:      `io\\backslash.txt`,
			expectedStorePath: func(workdir string) string {
				if runtime.GOOS == "windows" {
					return filepath.Join(workdir, "io", "backslash.txt")
				}
				return filepath.Join(workdir, `io\\backslash.txt`)
			},
		},
		{
			name:      "planned file endpoint",
			decorator: "@file",
			argKey:    "path",
			path:      "io/bare.txt",
			expectedStorePath: func(workdir string) string {
				return filepath.Join(workdir, "io", "bare.txt")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			workdir := t.TempDir()
			if err := os.Mkdir(filepath.Join(workdir, "io"), 0o700); err != nil {
				t.Fatal(err)
			}
			session := decorator.NewLocalSession().WithWorkdir(workdir)
			execCtx := decorator.ExecContext{Context: context.Background(), Session: session}

			target := &planfmt.CommandNode{
				Decorator: tt.decorator,
				Args: []planfmt.Arg{{
					Key: tt.argKey,
					Val: planfmt.Value{Kind: planfmt.ValueString, Str: tt.path},
				}},
			}

			ioDecorator, identity, ok := resolvePlanIOSink(target, os.Stderr)
			if diff := cmp.Diff(true, ok); diff != "" {
				t.Fatalf("expected target resolution success (-want +got):\n%s", diff)
			}

			expectedIdentity := "@file(" + tt.path + ")"
			if diff := cmp.Diff(expectedIdentity, identity); diff != "" {
				t.Fatalf("sink identity mismatch (-want +got):\n%s", diff)
			}

			writer, err := ioDecorator.(decorator.Sink).OpenWrite(execCtx, false)
			if err != nil {
				t.Fatalf("open sink writer: %v", err)
			}
			if _, err := writer.Write([]byte("payload\n")); err != nil {
				t.Fatalf("write sink payload: %v", err)
			}
			if err := writer.Finish(context.Background()); err != nil {
				t.Fatalf("close sink writer: %v", err)
			}

			storedPath := tt.expectedStorePath(workdir)
			content, err := session.Get(context.Background(), storedPath)
			if err != nil {
				t.Fatalf("read redirected content at resolved path: %v", err)
			}
			if diff := cmp.Diff("payload\n", string(content)); diff != "" {
				t.Fatalf("redirect sink content mismatch (-want +got):\n%s", diff)
			}

			reader, err := ioDecorator.(decorator.Source).OpenRead(execCtx)
			if err != nil {
				t.Fatalf("open sink reader: %v", err)
			}
			readContent, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("read sink source: %v", err)
			}
			if err := reader.Close(); err != nil {
				t.Fatalf("close sink reader: %v", err)
			}
			if diff := cmp.Diff("payload\n", string(readContent)); diff != "" {
				t.Fatalf("redirect source content mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRedirectOperatorSet(t *testing.T) {
	t.Parallel()

	operatorByMode := map[planfmt.RedirectMode]string{
		planfmt.RedirectOverwrite: ">",
		planfmt.RedirectAppend:    ">>",
		planfmt.RedirectInput:     "<",
	}

	want := map[planfmt.RedirectMode]string{
		planfmt.RedirectOverwrite: ">",
		planfmt.RedirectAppend:    ">>",
		planfmt.RedirectInput:     "<",
	}
	if diff := cmp.Diff(want, operatorByMode); diff != "" {
		t.Fatalf("redirect operator set mismatch (-want +got):\n%s", diff)
	}

	for _, operator := range operatorByMode {
		if diff := cmp.Diff(false, operator == "2>"); diff != "" {
			t.Fatalf("unexpected platform-specific redirect operator support (-want +got):\n%s", diff)
		}
	}
}

func TestRedirectStderrFlagDetectionIgnoresShell2RedirectSyntax(t *testing.T) {
	t.Parallel()

	withShellTwoRedirect := &planfmt.CommandNode{
		Decorator: "@shell",
		Args: []planfmt.Arg{{
			Key: "command",
			Val: planfmt.Value{Kind: planfmt.ValueString, Str: "echo output 2>stderr.txt"},
		}},
	}

	if diff := cmp.Diff(false, planRedirectStderrEnabled(withShellTwoRedirect)); diff != "" {
		t.Fatalf("stderr routing should not infer platform-specific 2> syntax (-want +got):\n%s", diff)
	}

	withExplicitStderr := &planfmt.CommandNode{
		Decorator: "@shell",
		Args: []planfmt.Arg{
			{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "echo output"}},
			{Key: "stderr", Val: planfmt.Value{Kind: planfmt.ValueBool, Bool: true}},
		},
	}
	if diff := cmp.Diff(true, planRedirectStderrEnabled(withExplicitStderr)); diff != "" {
		t.Fatalf("stderr routing should require explicit stderr=true flag (-want +got):\n%s", diff)
	}
}
