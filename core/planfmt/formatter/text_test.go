package formatter_test

import (
	"testing"

	"github.com/builtwithtofu/sigil/core/planfmt"
	"github.com/builtwithtofu/sigil/core/planfmt/formatter"
	"github.com/google/go-cmp/cmp"
)

// TestFormatStep verifies step formatting with trees
func TestFormatStep(t *testing.T) {
	tests := []struct {
		name     string
		step     planfmt.Step
		expected string
	}{
		{
			name: "simple shell command",
			step: planfmt.Step{
				ID: 1,
				Tree: &planfmt.CommandNode{
					Decorator: "@shell",
					Args: []planfmt.Arg{
						{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: `echo "Hello"`}},
					},
				},
			},
			expected: `@shell echo "Hello"`,
		},
		{
			name: "command with AND operator",
			step: planfmt.Step{
				ID: 1,
				Tree: &planfmt.AndNode{
					Left: &planfmt.CommandNode{
						Decorator: "@shell",
						Args: []planfmt.Arg{
							{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: `echo "A"`}},
						},
					},
					Right: &planfmt.CommandNode{
						Decorator: "@shell",
						Args: []planfmt.Arg{
							{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: `echo "B"`}},
						},
					},
				},
			},
			expected: `@shell echo "A" && @shell echo "B"`,
		},
		{
			name: "pipeline",
			step: planfmt.Step{
				ID: 1,
				Tree: &planfmt.PipelineNode{
					Commands: []planfmt.ExecutionNode{
						&planfmt.CommandNode{
							Decorator: "@shell",
							Args: []planfmt.Arg{
								{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "echo hello"}},
							},
						},
						&planfmt.CommandNode{
							Decorator: "@shell",
							Args: []planfmt.Arg{
								{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "grep hello"}},
							},
						},
					},
				},
			},
			expected: `@shell echo hello | @shell grep hello`,
		},
		{
			name: "redirect overwrite",
			step: planfmt.Step{
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
			expected: `@shell echo hello > @shell output.txt`,
		},
		{
			name: "for loop logic",
			step: planfmt.Step{
				ID: 1,
				Tree: &planfmt.LogicNode{
					Kind:      "for",
					Condition: `region in ["us","eu"]`,
					Result:    "region = us (iteration 1)",
				},
			},
			expected: `for region in ["us","eu"] -> region = us (iteration 1)`,
		},
		{
			name: "call trace logic",
			step: planfmt.Step{
				ID: 1,
				Tree: &planfmt.LogicNode{
					Kind:      "call",
					Condition: "deploy(prod, token=sigil:abc123)",
				},
			},
			expected: `deploy(prod, token=sigil:abc123)`,
		},
		{
			name: "retry decorator",
			step: planfmt.Step{
				ID: 1,
				Tree: &planfmt.CommandNode{
					Decorator: "@exec.retry",
					Args: []planfmt.Arg{
						{Key: "times", Val: planfmt.Value{Kind: planfmt.ValueInt, Int: 3}},
					},
				},
			},
			expected: `@exec.retry(times=3)`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatter.FormatStep(&tt.step)
			if diff := cmp.Diff(tt.expected, result); diff != "" {
				t.Errorf("FormatStep() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestFormat verifies full plan formatting
func TestFormat(t *testing.T) {
	plan := &planfmt.Plan{
		Target: "deploy",
		Steps: []planfmt.Step{
			{
				ID: 1,
				Tree: &planfmt.CommandNode{
					Decorator: "@shell",
					Args: []planfmt.Arg{
						{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "npm build"}},
					},
				},
			},
			{
				ID: 2,
				Tree: &planfmt.CommandNode{
					Decorator: "@shell",
					Args: []planfmt.Arg{
						{Key: "command", Val: planfmt.Value{Kind: planfmt.ValueString, Str: "docker build"}},
					},
				},
			},
		},
	}

	result := formatter.Format(plan)
	expected := "target: deploy\nstep 1: @shell npm build\nstep 2: @shell docker build\n"
	if diff := cmp.Diff(expected, result); diff != "" {
		t.Errorf("Format() mismatch (-want +got):\n%s", diff)
	}
}
