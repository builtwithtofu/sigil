package executor

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/builtwithtofu/sigil/core/decorator"
	"github.com/builtwithtofu/sigil/core/planfmt"
	_ "github.com/builtwithtofu/sigil/runtime/decorators"
	"github.com/google/go-cmp/cmp"
)

type testChdirDecorator struct{}

func (d *testChdirDecorator) Descriptor() decorator.Descriptor {
	return decorator.NewDescriptor("test.chdir").
		Summary("Test-only decorator that changes session workdir for its block").
		Roles(decorator.RoleWrapper).
		ParamString("dir", "Workdir for nested block").
		Required().
		Done().
		Block(decorator.BlockRequired).
		Build()
}

func (d *testChdirDecorator) Wrap(next decorator.ExecNode, params map[string]any) decorator.ExecNode {
	return &testChdirNode{next: next, params: params}
}

type testChdirNode struct {
	next   decorator.ExecNode
	params map[string]any
}

func (n *testChdirNode) Execute(ctx decorator.ExecContext) (decorator.Result, error) {
	if n.next == nil {
		return decorator.Result{ExitCode: 0}, nil
	}

	dir, _ := n.params["dir"].(string)
	if dir == "" {
		return decorator.Result{ExitCode: 1}, nil
	}

	child := ctx.WithSession(ctx.Session.WithWorkdir(dir))
	return n.next.Execute(child)
}

var registerTestChdirDecoratorOnce sync.Once

func registerTestChdirDecorator(t *testing.T) {
	t.Helper()
	var registerErr error
	registerTestChdirDecoratorOnce.Do(func() {
		registerErr = decorator.Register("test.chdir", &testChdirDecorator{})
	})
	if registerErr != nil {
		t.Fatalf("register @test.chdir failed: %v", registerErr)
	}
}

func TestParallelBranchWorkdirIsolation(t *testing.T) {
	t.Parallel()

	registerTestChdirDecorator(t)
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	dirA := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "parallel-output.txt")

	plan := &planfmt.Plan{Target: "parallel-workdir", Steps: []planfmt.Step{{
		ID: 1,
		Tree: &planfmt.RedirectNode{
			Source: &planfmt.CommandNode{
				Decorator: "@exec.parallel",
				Args: []planfmt.Arg{
					{Key: "maxConcurrency", Val: planfmt.Value{Kind: planfmt.ValueInt, Int: 2}},
					{Key: "onFailure", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "wait_all"}},
				},
				Block: []planfmt.Step{
					{
						ID: 2,
						Tree: &planfmt.CommandNode{
							Decorator: "@test.chdir",
							Args:      []planfmt.Arg{{Key: "dir", Val: planfmt.Value{Kind: planfmt.ValueString, Str: dirA}}},
							Block: []planfmt.Step{{
								ID:   3,
								Tree: &planfmt.CommandNode{Decorator: "@shell", Args: []planfmt.Arg{{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "pwd"}}}},
							}},
						},
					},
					{
						ID:   4,
						Tree: &planfmt.CommandNode{Decorator: "@shell", Args: []planfmt.Arg{{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "pwd"}}}},
					},
				},
			},
			Target: fileEndpoint(outPath),
			Mode:   planfmt.RedirectOverwrite,
		},
	}}}

	result, err := ExecutePlan(context.Background(), plan, Config{}, testVault())
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if diff := cmp.Diff(0, result.ExitCode); diff != "" {
		t.Fatalf("exit code mismatch (-want +got):\n%s", diff)
	}

	want := dirA + "\n" + originalWd + "\n"
	content, readErr := os.ReadFile(outPath)
	if readErr != nil {
		t.Fatalf("read redirected output: %v", readErr)
	}
	if diff := cmp.Diff(want, string(content)); diff != "" {
		t.Fatalf("parallel branch isolation mismatch (-want +got):\n%s", diff)
	}
}
