package formatter

import (
	"bytes"
	"strings"
	"testing"

	"github.com/builtwithtofu/sigil/core/planfmt"
	"github.com/google/go-cmp/cmp"
)

func TestFormatTree_EmptyPlan(t *testing.T) {
	plan := &planfmt.Plan{
		Target: "test",
		Steps:  []planfmt.Step{},
	}

	var buf bytes.Buffer
	FormatTree(&buf, plan, false)

	expected := "test:\n(no steps)\n"
	if diff := cmp.Diff(expected, buf.String()); diff != "" {
		t.Errorf("Output mismatch (-want +got):\n%s", diff)
	}
}

func TestFormatTree_SingleStep(t *testing.T) {
	plan := &planfmt.Plan{
		Target: "deploy",
		Steps: []planfmt.Step{
			{
				ID: 1,
				Tree: &planfmt.CommandNode{
					Decorator: "shell",
					Args: []planfmt.Arg{
						{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "echo hello"}},
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	FormatTree(&buf, plan, false)

	output := buf.String()
	if !strings.Contains(output, "deploy:") {
		t.Error("Expected target name 'deploy:'")
	}
	if !strings.Contains(output, "└─ shell echo hello") {
		t.Errorf("Expected tree output with shell command, got:\n%s", output)
	}
}

func TestFormatTree_MultipleSteps(t *testing.T) {
	plan := &planfmt.Plan{
		Target: "build",
		Steps: []planfmt.Step{
			{
				ID: 1,
				Tree: &planfmt.CommandNode{
					Decorator: "shell",
					Args: []planfmt.Arg{
						{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "go build"}},
					},
				},
			},
			{
				ID: 2,
				Tree: &planfmt.CommandNode{
					Decorator: "shell",
					Args: []planfmt.Arg{
						{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "go test"}},
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	FormatTree(&buf, plan, false)

	output := buf.String()
	if !strings.Contains(output, "build:") {
		t.Error("Expected target name 'build:'")
	}
	// First step should use ├─ (not last)
	if !strings.Contains(output, "├─") {
		t.Error("Expected ├─ for first step")
	}
	// Last step should use └─
	if !strings.Contains(output, "└─") {
		t.Error("Expected └─ for last step")
	}
}

func TestFormatTree_WithOperators(t *testing.T) {
	plan := &planfmt.Plan{
		Target: "deploy",
		Steps: []planfmt.Step{
			{
				ID: 1,
				Tree: &planfmt.AndNode{
					Left: &planfmt.CommandNode{
						Decorator: "shell",
						Args: []planfmt.Arg{
							{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "npm build"}},
						},
					},
					Right: &planfmt.CommandNode{
						Decorator: "shell",
						Args: []planfmt.Arg{
							{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "docker build"}},
						},
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	FormatTree(&buf, plan, false)

	output := buf.String()
	if !strings.Contains(output, "&&") {
		t.Errorf("Expected && operator in output, got:\n%s", output)
	}
	if !strings.Contains(output, "npm build") {
		t.Error("Expected first command")
	}
	if !strings.Contains(output, "docker build") {
		t.Error("Expected second command")
	}
}

func TestFormatTree_WithCallTrace(t *testing.T) {
	plan := &planfmt.Plan{
		Target: "deploy",
		Steps: []planfmt.Step{
			{
				ID: 1,
				Tree: &planfmt.LogicNode{
					Kind:      "call",
					Condition: "deploy(prod, token=sigil:abc123)",
					Block: []planfmt.Step{
						{
							ID: 2,
							Tree: &planfmt.CommandNode{
								Decorator: "@shell",
								Args:      []planfmt.Arg{{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "echo ok"}}},
							},
						},
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	FormatTree(&buf, plan, false)

	output := buf.String()
	if !strings.Contains(output, "deploy(prod, token=sigil:abc123)") {
		t.Fatalf("Expected call trace label, got:\n%s", output)
	}
	if strings.Contains(output, "call deploy(prod, token=sigil:abc123)") {
		t.Fatalf("Expected signature-only call trace label, got:\n%s", output)
	}
	if !strings.Contains(output, "@shell echo ok") {
		t.Fatalf("Expected nested command in call trace, got:\n%s", output)
	}
	if strings.Contains(output, "super-secret") {
		t.Fatalf("Output leaked redacted value:\n%s", output)
	}
}

func TestFormatTree_WithPipeline(t *testing.T) {
	plan := &planfmt.Plan{
		Target: "test",
		Steps: []planfmt.Step{
			{
				ID: 1,
				Tree: &planfmt.PipelineNode{
					Commands: []planfmt.ExecutionNode{
						&planfmt.CommandNode{
							Decorator: "shell",
							Args: []planfmt.Arg{
								{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "echo hello"}},
							},
						},
						&planfmt.CommandNode{
							Decorator: "shell",
							Args: []planfmt.Arg{
								{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "grep hello"}},
							},
						},
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	FormatTree(&buf, plan, false)

	output := buf.String()
	if !strings.Contains(output, "|") {
		t.Errorf("Expected | operator in output, got:\n%s", output)
	}
	if !strings.Contains(output, "echo hello") {
		t.Error("Expected first command")
	}
	if !strings.Contains(output, "grep hello") {
		t.Error("Expected second command")
	}
}

func TestFormatTree_WithRedirect(t *testing.T) {
	plan := &planfmt.Plan{
		Target: "test",
		Steps: []planfmt.Step{
			{
				ID: 1,
				Tree: &planfmt.RedirectNode{
					Mode: planfmt.RedirectOverwrite,
					Source: &planfmt.CommandNode{
						Decorator: "@shell",
						Args: []planfmt.Arg{
							{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "echo hello"}},
						},
					},
					Target: planfmt.EndpointSpec{
						Decorator: "@shell",
						Args: []planfmt.Arg{
							{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "output.txt"}},
						},
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	FormatTree(&buf, plan, false)

	output := buf.String()
	if !strings.Contains(output, ">") {
		t.Errorf("Expected redirect operator in output, got:\n%s", output)
	}
	if !strings.Contains(output, "echo hello") {
		t.Errorf("Expected source command, got:\n%s", output)
	}
	if !strings.Contains(output, "output.txt") {
		t.Errorf("Expected target command, got:\n%s", output)
	}
}

func TestFormatTree_ForLoopGrouped(t *testing.T) {
	plan := &planfmt.Plan{
		Target: "deploy",
		Steps: []planfmt.Step{
			{
				ID: 1,
				Tree: &planfmt.LogicNode{
					Kind:      "for",
					Condition: `region in ["us","eu"]`,
					Result:    "region = us (iteration 1)",
					Block: []planfmt.Step{
						{
							ID: 2,
							Tree: &planfmt.CommandNode{
								Decorator: "@shell",
								Args: []planfmt.Arg{
									{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "echo one"}},
								},
							},
						},
					},
				},
			},
			{
				ID: 3,
				Tree: &planfmt.LogicNode{
					Kind:      "for",
					Condition: `region in ["us","eu"]`,
					Result:    "region = eu (iteration 2)",
					Block: []planfmt.Step{
						{
							ID: 4,
							Tree: &planfmt.CommandNode{
								Decorator: "@shell",
								Args: []planfmt.Arg{
									{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "echo two"}},
								},
							},
						},
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	FormatTree(&buf, plan, false)

	output := buf.String()
	expected := "deploy:\n" +
		"└─ for region in [\"us\",\"eu\"]: 2 iterations\n" +
		"   ├─ [1] region = us: @shell echo one\n" +
		"   └─ [2] region = eu: @shell echo two\n"
	if diff := cmp.Diff(expected, output); diff != "" {
		t.Errorf("Output mismatch (-want +got):\n%s", diff)
	}
}

func TestFormatTree_WithColor(t *testing.T) {
	plan := &planfmt.Plan{
		Target: "test",
		Steps: []planfmt.Step{
			{
				ID: 1,
				Tree: &planfmt.CommandNode{
					Decorator: "shell",
					Args: []planfmt.Arg{
						{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "echo test"}},
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	FormatTree(&buf, plan, true) // Enable color

	output := buf.String()
	// Should contain ANSI color codes
	if !strings.Contains(output, "\033[") {
		t.Error("Expected ANSI color codes when color is enabled")
	}
}
