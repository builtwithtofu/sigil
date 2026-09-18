package executor

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/builtwithtofu/sigil/core/decorator"
	"github.com/builtwithtofu/sigil/core/planfmt"
	_ "github.com/builtwithtofu/sigil/runtime/decorators"
	"github.com/google/go-cmp/cmp"
)

func TestBarePathEquivalence(t *testing.T) {
	t.Parallel()

	t.Run("output redirect bare path equals explicit file decorator", func(t *testing.T) {
		t.Parallel()

		workdir := t.TempDir()
		if err := os.Mkdir(filepath.Join(workdir, "io"), 0o700); err != nil {
			t.Fatal(err)
		}
		session := decorator.NewLocalSession().WithWorkdir(workdir)
		execCtx := decorator.ExecContext{Context: context.Background(), Session: session}

		relPath := filepath.Join("io", "out.txt")
		p, _ := planSinkScript(t, "echo output > "+relPath)
		bareTarget := p.Steps[0].Tree.(*planfmt.RedirectNode).Target.Command()
		explicitTarget := &planfmt.CommandNode{
			Decorator: "@file",
			Args: []planfmt.Arg{{
				Key: "path",
				Val: planfmt.Value{Kind: planfmt.ValueString, Str: relPath},
			}},
		}

		bareIO, bareIdentity, ok := resolvePlanIOSink(bareTarget, os.Stderr)
		if !ok {
			t.Fatal("expected bare target to resolve")
		}
		explicitIO, explicitIdentity, ok := resolvePlanIOSink(explicitTarget, os.Stderr)
		if !ok {
			t.Fatal("expected explicit target to resolve")
		}

		if diff := cmp.Diff(explicitIdentity, bareIdentity); diff != "" {
			t.Fatalf("sink identity mismatch (-want +got):\n%s", diff)
		}

		bareWriter, err := bareIO.(decorator.Sink).OpenWrite(execCtx, false)
		if err != nil {
			t.Fatalf("open bare writer: %v", err)
		}
		if _, err := bareWriter.Write([]byte("bare\n")); err != nil {
			t.Fatalf("write bare output: %v", err)
		}
		if err := bareWriter.Finish(context.Background()); err != nil {
			t.Fatalf("close bare writer: %v", err)
		}

		explicitWriter, err := explicitIO.(decorator.Sink).OpenWrite(execCtx, false)
		if err != nil {
			t.Fatalf("open explicit writer: %v", err)
		}
		if _, err := explicitWriter.Write([]byte("explicit\n")); err != nil {
			t.Fatalf("write explicit output: %v", err)
		}
		if err := explicitWriter.Finish(context.Background()); err != nil {
			t.Fatalf("close explicit writer: %v", err)
		}

		actual, err := session.Get(context.Background(), filepath.Join(workdir, relPath))
		if err != nil {
			t.Fatalf("read redirected output: %v", err)
		}
		if diff := cmp.Diff("explicit\n", string(actual)); diff != "" {
			t.Fatalf("redirected output mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("input source bare path equals explicit file decorator", func(t *testing.T) {
		t.Parallel()

		workdir := t.TempDir()
		session := decorator.NewLocalSession().WithWorkdir(workdir)
		execCtx := decorator.ExecContext{Context: context.Background(), Session: session}

		relPath := filepath.Join("io", "in.txt")
		expected := "from-input\n"
		if err := session.Put(context.Background(), []byte(expected), filepath.Join(workdir, relPath), 0o644); err != nil {
			t.Fatalf("seed input file: %v", err)
		}

		p, _ := planSinkScript(t, "cat < "+relPath)
		bareTarget := p.Steps[0].Tree.(*planfmt.RedirectNode).Target.Command()
		explicitTarget := &planfmt.CommandNode{
			Decorator: "@file",
			Args: []planfmt.Arg{{
				Key: "path",
				Val: planfmt.Value{Kind: planfmt.ValueString, Str: relPath},
			}},
		}

		bareIO, bareIdentity, ok := resolvePlanIOSink(bareTarget, os.Stderr)
		if !ok {
			t.Fatal("expected bare target to resolve")
		}
		explicitIO, explicitIdentity, ok := resolvePlanIOSink(explicitTarget, os.Stderr)
		if !ok {
			t.Fatal("expected explicit target to resolve")
		}

		if diff := cmp.Diff(explicitIdentity, bareIdentity); diff != "" {
			t.Fatalf("source identity mismatch (-want +got):\n%s", diff)
		}

		bareReader, err := bareIO.(decorator.Source).OpenRead(execCtx)
		if err != nil {
			t.Fatalf("open bare reader: %v", err)
		}
		bareData, err := io.ReadAll(bareReader)
		if err != nil {
			t.Fatalf("read bare source: %v", err)
		}
		if err := bareReader.Close(); err != nil {
			t.Fatalf("close bare reader: %v", err)
		}

		explicitReader, err := explicitIO.(decorator.Source).OpenRead(execCtx)
		if err != nil {
			t.Fatalf("open explicit reader: %v", err)
		}
		explicitData, err := io.ReadAll(explicitReader)
		if err != nil {
			t.Fatalf("read explicit source: %v", err)
		}
		if err := explicitReader.Close(); err != nil {
			t.Fatalf("close explicit reader: %v", err)
		}

		if diff := cmp.Diff(expected, string(bareData)); diff != "" {
			t.Fatalf("bare source data mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(expected, string(explicitData)); diff != "" {
			t.Fatalf("explicit source data mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(string(explicitData), string(bareData)); diff != "" {
			t.Fatalf("source equivalence mismatch (-want +got):\n%s", diff)
		}
	})
}
