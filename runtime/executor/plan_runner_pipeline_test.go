package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/builtwithtofu/sigil/core/planfmt"
	_ "github.com/builtwithtofu/sigil/runtime/decorators"
	"github.com/google/go-cmp/cmp"
)

func TestPlanPipelineReturnsLastCommandExitCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		left     string
		right    string
		wantExit int
	}{
		{name: "left fails right succeeds", left: "exit 7", right: "exit 0", wantExit: 0},
		{name: "left succeeds right fails", left: "exit 0", right: "exit 9", wantExit: 9},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			plan := &planfmt.Plan{Target: "pipeline-exit", Steps: []planfmt.Step{{
				ID: 1,
				Tree: &planfmt.PipelineNode{Commands: []planfmt.ExecutionNode{
					planShellCommand(tt.left),
					planShellCommand(tt.right),
				}},
			}}}

			result, err := ExecutePlan(context.Background(), plan, Config{}, testVault())
			if err != nil {
				t.Fatalf("execute failed: %v", err)
			}
			if diff := cmp.Diff(tt.wantExit, result.ExitCode); diff != "" {
				t.Fatalf("pipeline exit mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPlanPipelinePreservesDataFlow(t *testing.T) {
	t.Parallel()

	outPath := filepath.Join(t.TempDir(), "pipeline-result.txt")
	plan := &planfmt.Plan{Target: "pipeline-flow", Steps: []planfmt.Step{{
		ID: 1,
		Tree: &planfmt.RedirectNode{
			Source: &planfmt.PipelineNode{Commands: []planfmt.ExecutionNode{
				planShellCommand("printf 'alpha\\nbeta\\n'"),
				planShellCommand("awk 'END {print NR}'"),
			}},
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

	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read redirected output: %v", err)
	}
	if diff := cmp.Diff("2\n", string(content)); diff != "" {
		t.Fatalf("pipeline output mismatch (-want +got):\n%s", diff)
	}
}

func TestPlanPipelinePanicsOnUnsupportedNodeType(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for invalid pipeline node type")
		}
	}()

	plan := &planfmt.Plan{Target: "invalid-pipeline", Steps: []planfmt.Step{{
		ID: 1,
		Tree: &planfmt.PipelineNode{Commands: []planfmt.ExecutionNode{
			&planfmt.SequenceNode{Nodes: []planfmt.ExecutionNode{planShellCommand("echo unreachable")}},
		}},
	}}}

	_, _ = ExecutePlan(context.Background(), plan, Config{}, testVault())
}

func TestPlanPipelinePanicsOnNilNode(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for nil pipeline node")
		}
	}()

	plan := &planfmt.Plan{Target: "nil-pipeline", Steps: []planfmt.Step{{
		ID: 1,
		Tree: &planfmt.PipelineNode{Commands: []planfmt.ExecutionNode{
			nil,
		}},
	}}}

	_, _ = ExecutePlan(context.Background(), plan, Config{}, testVault())
}

func TestStderrCaptureWithFileSink(t *testing.T) {
	t.Run("defaults to stdout", func(t *testing.T) {
		outPath := filepath.Join(t.TempDir(), "stdout.txt")
		plan := &planfmt.Plan{Target: "redirect-stdout-file", Steps: []planfmt.Step{{
			ID: 1,
			Tree: &planfmt.RedirectNode{
				Source: &planfmt.CommandNode{
					Decorator: "@shell",
					Args:      []planfmt.Arg{{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "echo out && echo err 1>&2"}}},
				},
				Target: planfmt.EndpointSpec{
					Decorator: "@file",
					Args:      []planfmt.Arg{{Key: "path", Val: planfmt.Value{Kind: planfmt.ValueString, Str: outPath}}},
				},
				Mode: planfmt.RedirectOverwrite,
			},
		}}}

		result, err := ExecutePlan(context.Background(), plan, Config{}, testVault())
		if err != nil {
			t.Fatalf("execute failed: %v", err)
		}
		if diff := cmp.Diff(0, result.ExitCode); diff != "" {
			t.Fatalf("exit code mismatch (-want +got):\n%s", diff)
		}

		content, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatalf("read redirected output: %v", err)
		}
		if diff := cmp.Diff("out\n", string(content)); diff != "" {
			t.Fatalf("stdout redirect mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("routes stderr when enabled", func(t *testing.T) {
		outPath := filepath.Join(t.TempDir(), "stderr.txt")
		plan := &planfmt.Plan{Target: "redirect-stderr-file", Steps: []planfmt.Step{{
			ID: 1,
			Tree: &planfmt.RedirectNode{
				Source: &planfmt.CommandNode{
					Decorator: "@shell",
					Args: []planfmt.Arg{
						{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "echo out && echo err 1>&2"}},
						{Key: "stderr", Val: planfmt.Value{Kind: planfmt.ValueBool, Bool: true}},
					},
				},
				Target: planfmt.EndpointSpec{
					Decorator: "@file",
					Args:      []planfmt.Arg{{Key: "path", Val: planfmt.Value{Kind: planfmt.ValueString, Str: outPath}}},
				},
				Mode: planfmt.RedirectOverwrite,
			},
		}}}

		result, err := ExecutePlan(context.Background(), plan, Config{}, testVault())
		if err != nil {
			t.Fatalf("execute failed: %v", err)
		}
		if diff := cmp.Diff(0, result.ExitCode); diff != "" {
			t.Fatalf("exit code mismatch (-want +got):\n%s", diff)
		}

		content, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatalf("read redirected output: %v", err)
		}
		if diff := cmp.Diff("err\n", string(content)); diff != "" {
			t.Fatalf("stderr redirect mismatch (-want +got):\n%s", diff)
		}
	})
}

func planShellCommand(command string) *planfmt.CommandNode {
	return &planfmt.CommandNode{
		Decorator: "@shell",
		Args: []planfmt.Arg{{
			Key: "command",
			Val: planfmt.Value{Kind: planfmt.ValueString, Str: command},
		}},
	}
}
