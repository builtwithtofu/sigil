package planner

import (
	"fmt"
	"strings"
	"testing"

	"github.com/builtwithtofu/sigil/core/planfmt"
	"github.com/builtwithtofu/sigil/runtime/parser"
	"github.com/google/go-cmp/cmp"
)

// getPlanDecorator extracts the first command decorator name from a plan tree.
func getPlanDecorator(tree planfmt.ExecutionNode) string {
	if tree == nil {
		return ""
	}
	switch n := tree.(type) {
	case *planfmt.CommandNode:
		return n.Decorator
	case *planfmt.LogicNode:
		if len(n.Block) == 0 {
			return ""
		}
		return getPlanDecorator(n.Block[0].Tree)
	case *planfmt.TryNode:
		if len(n.TryBlock) == 0 {
			return ""
		}
		return getPlanDecorator(n.TryBlock[0].Tree)
	default:
		return ""
	}
}

// parseAndPlan is a helper that parses source and runs the new planner
func parseAndPlan(t *testing.T, source, target string) (*planfmt.Plan, error) {
	t.Helper()

	tree := parser.Parse([]byte(source))
	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	return Plan(tree.Events, tree.Tokens, Config{
		Target: target,
	})
}

func phase1FixedSalt() []byte {
	return []byte{
		1, 2, 3, 4, 5, 6, 7, 8,
		9, 10, 11, 12, 13, 14, 15, 16,
		17, 18, 19, 20, 21, 22, 23, 24,
		25, 26, 27, 28, 29, 30, 31, 32,
	}
}

func parseAndPlanWithFixedSalt(t *testing.T, source, target string) (*planfmt.Plan, error) {
	t.Helper()

	tree := parser.Parse([]byte(source))
	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	return Plan(tree.Events, tree.Tokens, Config{
		Target:   target,
		PlanSalt: phase1FixedSalt(),
	})
}

func treeShape(node planfmt.ExecutionNode) string {
	switch n := node.(type) {
	case *planfmt.CommandNode:
		key := "command"
		if n.Decorator == "@file" {
			key = "path"
		}
		return fmt.Sprintf("cmd(%s)", getCommandArg(n, key))
	case *planfmt.AndNode:
		return fmt.Sprintf("and(%s,%s)", treeShape(n.Left), treeShape(n.Right))
	case *planfmt.OrNode:
		return fmt.Sprintf("or(%s,%s)", treeShape(n.Left), treeShape(n.Right))
	case *planfmt.SequenceNode:
		parts := make([]string, len(n.Nodes))
		for i, child := range n.Nodes {
			parts[i] = treeShape(child)
		}
		return fmt.Sprintf("seq(%s)", strings.Join(parts, ","))
	case *planfmt.PipelineNode:
		parts := make([]string, len(n.Commands))
		for i, child := range n.Commands {
			parts[i] = treeShape(child)
		}
		return fmt.Sprintf("pipe(%s)", strings.Join(parts, ","))
	case *planfmt.RedirectNode:
		mode := ">"
		if n.Mode == planfmt.RedirectAppend {
			mode = ">>"
		}
		return fmt.Sprintf("redir(%s,%s,%s)", mode, treeShape(n.Source), treeShape(n.Target.Command()))
	default:
		return fmt.Sprintf("unknown(%T)", node)
	}
}

// =============================================================================
// Shell Command Tests
// =============================================================================

// TestParity_SimpleShellCommand tests basic echo command planning
func TestParity_SimpleShellCommand(t *testing.T) {
	source := `echo "Hello, World!"`

	plan, err := parseAndPlan(t, source, "")
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// Verify plan structure
	if len(plan.Steps) != 1 {
		t.Errorf("Expected 1 step, got %d", len(plan.Steps))
	}

	step := plan.Steps[0]
	if step.Tree == nil {
		t.Fatal("Expected tree, got nil")
	}

	// Tree should be a CommandNode with @shell decorator
	if getPlanDecorator(step.Tree) != "@shell" {
		t.Errorf("Expected @shell decorator, got %q", getPlanDecorator(step.Tree))
	}

	// Check command argument
	expectedCmd := `echo "Hello, World!"`
	if getCommandArg(step.Tree, "command") != expectedCmd {
		t.Errorf("Expected command %q, got %q", expectedCmd, getCommandArg(step.Tree, "command"))
	}
}

// TestParity_ShellCommandWithDashes tests commands with flag arguments
func TestParity_ShellCommandWithDashes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "wc with -l flag",
			input:    `wc -l`,
			expected: `wc -l`,
		},
		{
			name:     "echo with -n flag",
			input:    `echo -n "hello"`,
			expected: `echo -n "hello"`,
		},
		{
			name:     "ls with -la flags",
			input:    `ls -la`,
			expected: `ls -la`,
		},
		{
			name:     "kubectl with -f flag",
			input:    `kubectl apply -f deployment.yaml`,
			expected: `kubectl apply -f deployment.yaml`,
		},
		{
			name:     "grep with -v flag",
			input:    `grep -v "pattern"`,
			expected: `grep -v "pattern"`,
		},
		{
			name:     "double dash --",
			input:    `echo -- "end of options"`,
			expected: `echo -- "end of options"`,
		},
		{
			name:     "long flag --file",
			input:    `command --file config.yaml`,
			expected: `command --file config.yaml`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := parseAndPlan(t, tt.input, "")
			if err != nil {
				t.Fatalf("Plan failed: %v", err)
			}

			if len(plan.Steps) != 1 {
				t.Fatalf("Expected 1 step, got %d", len(plan.Steps))
			}

			step := plan.Steps[0]
			if getPlanDecorator(step.Tree) != "@shell" {
				t.Errorf("Expected @shell decorator, got %q", getPlanDecorator(step.Tree))
			}

			actual := getCommandArg(step.Tree, "command")
			if actual != tt.expected {
				t.Errorf("Expected command %q, got %q", tt.expected, actual)
			}
		})
	}
}

// TestParity_MultipleShellCommands tests planning multiple commands
func TestParity_MultipleShellCommands(t *testing.T) {
	source := `echo "First"
echo "Second"
echo "Third"`

	plan, err := parseAndPlan(t, source, "")
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// Should have 3 steps
	if len(plan.Steps) != 3 {
		t.Fatalf("Expected 3 steps, got %d", len(plan.Steps))
	}

	// Verify each step has correct command
	expectedCommands := []string{`echo "First"`, `echo "Second"`, `echo "Third"`}
	for i, step := range plan.Steps {
		if getPlanDecorator(step.Tree) != "@shell" {
			t.Errorf("Step %d: Expected @shell decorator, got %q", i, getPlanDecorator(step.Tree))
		}

		actual := getCommandArg(step.Tree, "command")
		if actual != expectedCommands[i] {
			t.Errorf("Step %d: Expected command %q, got %q", i, expectedCommands[i], actual)
		}
	}
}

// =============================================================================
// Function Mode Tests
// =============================================================================

// TestParity_FunctionDefinition tests planning function definitions
func TestParity_FunctionDefinition(t *testing.T) {
	source := `fun hello = echo "Hello"
fun world = echo "World"`

	tree := parser.Parse([]byte(source))
	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	// Plan in script mode with only functions - should produce empty plan
	// (functions are only executed when targeted in command mode)
	plan, err := Plan(tree.Events, tree.Tokens, Config{
		Target: "",
	})
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// In script mode with only functions, plan should be empty (valid but no steps)
	if len(plan.Steps) != 0 {
		t.Errorf("Expected 0 steps in script mode with only functions, got %d", len(plan.Steps))
	}
}

// TestParity_CommandModeTargetSelection tests targeting a specific function
func TestParity_CommandModeTargetSelection(t *testing.T) {
	source := `fun hello = echo "Hello"
fun world = echo "World"
fun deploy = kubectl apply -f k8s/`

	tree := parser.Parse([]byte(source))
	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	// Plan targeting "hello" function
	plan, err := Plan(tree.Events, tree.Tokens, Config{
		Target: "hello",
	})
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// Should target hello function
	if plan.Target != "hello" {
		t.Errorf("Expected target 'hello', got %q", plan.Target)
	}

	// Should have 1 step (the echo from hello)
	if len(plan.Steps) != 1 {
		t.Fatalf("Expected 1 step for hello function, got %d", len(plan.Steps))
	}

	// Verify it's the right command
	cmd := getCommandArg(plan.Steps[0].Tree, "command")
	expectedCmd := `echo "Hello"`
	if cmd != expectedCmd {
		t.Errorf("Expected command %q, got %q", expectedCmd, cmd)
	}
}

// TestParity_TargetNotFound tests error when target doesn't exist
func TestParity_TargetNotFound(t *testing.T) {
	source := `fun hello = echo "Hello"`

	tree := parser.Parse([]byte(source))
	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	_, err := Plan(tree.Events, tree.Tokens, Config{
		Target: "nonexistent",
	})

	if err == nil {
		t.Fatal("Expected error for non-existent target")
	}
}

func TestParity_ScriptModeTopLevelFunctionCallExecutes(t *testing.T) {
	source := `fun helper(name String) {
	echo @var.name
}

helper("prod")`

	plan, err := parseAndPlan(t, source, "")
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	if len(plan.Steps) != 1 {
		t.Fatalf("Expected 1 step, got %d", len(plan.Steps))
	}

	logic, ok := plan.Steps[0].Tree.(*planfmt.LogicNode)
	if !ok {
		t.Fatalf("Expected LogicNode call trace, got %T", plan.Steps[0].Tree)
	}
	if diff := cmp.Diff("call", logic.Kind); diff != "" {
		t.Fatalf("call trace kind mismatch (-want +got):\n%s", diff)
	}
	if !strings.Contains(logic.Condition, "helper") {
		t.Fatalf("Expected call trace to reference helper, got %q", logic.Condition)
	}

	if diff := cmp.Diff("@shell", getPlanDecorator(plan.Steps[0].Tree)); diff != "" {
		t.Fatalf("step decorator mismatch (-want +got):\n%s", diff)
	}
}

func TestParity_CommandModeTargetFunctionCallExecutes(t *testing.T) {
	source := `fun helper(name String) {
	echo @var.name
}

fun deploy(env String = "prod") {
	helper(@var.env)
}`

	plan, err := parseAndPlan(t, source, "deploy")
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	if len(plan.Steps) != 1 {
		t.Fatalf("Expected 1 step, got %d", len(plan.Steps))
	}

	if diff := cmp.Diff("deploy", plan.Target); diff != "" {
		t.Fatalf("plan target mismatch (-want +got):\n%s", diff)
	}

	logic, ok := plan.Steps[0].Tree.(*planfmt.LogicNode)
	if !ok {
		t.Fatalf("Expected LogicNode call trace, got %T", plan.Steps[0].Tree)
	}
	if diff := cmp.Diff("call", logic.Kind); diff != "" {
		t.Fatalf("call trace kind mismatch (-want +got):\n%s", diff)
	}
	if !strings.Contains(logic.Condition, "helper") {
		t.Fatalf("Expected call trace to reference helper, got %q", logic.Condition)
	}

	if diff := cmp.Diff("@shell", getPlanDecorator(plan.Steps[0].Tree)); diff != "" {
		t.Fatalf("step decorator mismatch (-want +got):\n%s", diff)
	}
}

// =============================================================================
// Plan Structure Tests
// =============================================================================

// TestParity_EmptyPlan tests planning empty source
func TestParity_EmptyPlan(t *testing.T) {
	source := ``

	plan, err := parseAndPlan(t, source, "")
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// Should have 0 steps
	if len(plan.Steps) != 0 {
		t.Errorf("Expected 0 steps for empty plan, got %d", len(plan.Steps))
	}
}

// TestParity_StepIDUniqueness tests that step IDs are unique
func TestParity_StepIDUniqueness(t *testing.T) {
	source := `echo "First"
echo "Second"
echo "Third"`

	plan, err := parseAndPlan(t, source, "")
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	if len(plan.Steps) != 3 {
		t.Fatalf("Expected 3 steps, got %d", len(plan.Steps))
	}

	// Verify IDs are sequential starting from 1
	for i, step := range plan.Steps {
		expectedID := uint64(i + 1)
		if step.ID != expectedID {
			t.Errorf("Step %d: Expected ID %d, got %d", i, expectedID, step.ID)
		}
	}
}

// =============================================================================
// Operator Tests
// =============================================================================

// TestParity_ShellCommandWithOperators tests && and || operators
func TestParity_ShellCommandWithOperators(t *testing.T) {
	source := `echo "first" && echo "second"`

	plan, err := parseAndPlan(t, source, "")
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// Operators chain commands into a single step with AndNode
	if len(plan.Steps) != 1 {
		t.Fatalf("Expected 1 step for chained commands, got %d", len(plan.Steps))
	}

	step := plan.Steps[0]

	// Check if it's an AndNode (representing &&)
	andNode, ok := step.Tree.(*planfmt.AndNode)
	if !ok {
		t.Fatalf("Expected AndNode for && chain, got %T", step.Tree)
	}

	// Verify both sides are CommandNodes
	if andNode.Left == nil || andNode.Right == nil {
		t.Error("AndNode should have both left and right children")
	}

	// Verify left and right are @shell commands
	_, leftOk := andNode.Left.(*planfmt.CommandNode)
	_, rightOk := andNode.Right.(*planfmt.CommandNode)
	if !leftOk || !rightOk {
		t.Errorf("AndNode children should be CommandNodes: left=%T, right=%T", andNode.Left, andNode.Right)
	}
}

// TestParity_MultipleSteps tests multiple independent steps
func TestParity_MultipleSteps(t *testing.T) {
	source := `echo "step1"
echo "step2"
echo "step3"`

	plan, err := parseAndPlan(t, source, "")
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	if len(plan.Steps) != 3 {
		t.Fatalf("Expected 3 steps, got %d", len(plan.Steps))
	}

	// Each step should be a CommandNode
	for i, step := range plan.Steps {
		if getPlanDecorator(step.Tree) != "@shell" {
			t.Errorf("Step %d: Expected @shell decorator, got %q", i, getPlanDecorator(step.Tree))
		}
	}
}

// =============================================================================
// Contract/Plan Salt Tests
// =============================================================================

// TestParity_PlanSalt tests that plan has salt for contract verification
func TestParity_PlanSalt(t *testing.T) {
	source := `echo "test"`

	plan, err := parseAndPlan(t, source, "")
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// Verify plan has salt
	if len(plan.PlanSalt) == 0 {
		t.Error("Expected plan salt to be set")
	}
}

func TestParity_PreludeResolverParity_ScriptVsCommand(t *testing.T) {
	scriptSource := `
var ENV = "prod"
if true {
    var NAME = "service"
} else {
    var NAME = "fallback"
}
echo "@var.ENV:@var.NAME"
`

	commandSource := `
var ENV = "prod"
if true {
    var NAME = "service"
} else {
    var NAME = "fallback"
}
fun deploy {
    echo "@var.ENV:@var.NAME"
}
`

	_, scriptErr := parseAndPlanWithFixedSalt(t, scriptSource, "")
	if scriptErr == nil {
		t.Fatal("Expected script mode undefined variable error for NAME")
	}

	_, commandErr := parseAndPlanWithFixedSalt(t, commandSource, "deploy")
	if commandErr == nil {
		t.Fatal("Expected command mode undefined variable error for NAME")
	}

	if diff := cmp.Diff(scriptErr.Error(), commandErr.Error()); diff != "" {
		t.Errorf("Prelude parity error mismatch (-script +command):\n%s", diff)
	}
}

func TestParity_PreludeResolverParity_UndefinedVar(t *testing.T) {
	scriptSource := `
echo "@var.ENV"
var ENV = "prod"
`

	commandSource := `
fun deploy {
    echo "@var.ENV"
}
var ENV = "prod"
`

	_, scriptErr := parseAndPlanWithFixedSalt(t, scriptSource, "")
	if scriptErr == nil {
		t.Fatal("Expected script mode undefined variable error")
	}

	_, commandErr := parseAndPlanWithFixedSalt(t, commandSource, "deploy")
	if commandErr == nil {
		t.Fatal("Expected command mode undefined variable error")
	}

	if diff := cmp.Diff(scriptErr.Error(), commandErr.Error()); diff != "" {
		t.Errorf("Undefined variable error mismatch (-script +command):\n%s", diff)
	}
}

func TestParity_OperatorPrecedenceShapes(t *testing.T) {
	tests := []struct {
		name  string
		src   string
		shape string
	}{
		{
			name:  "and before or before semicolon",
			src:   `a && b || c; d`,
			shape: `seq(or(and(cmd(a),cmd(b)),cmd(c)),cmd(d))`,
		},
		{
			name:  "redirect before logical operators",
			src:   `a > out && b || c`,
			shape: `or(and(redir(>,cmd(a),cmd(out)),cmd(b)),cmd(c))`,
		},
		{
			name:  "pipe before or",
			src:   `a | b || c`,
			shape: `or(pipe(cmd(a),cmd(b)),cmd(c))`,
		},
		{
			name:  "append redirect before semicolon",
			src:   `a >> out; b`,
			shape: `seq(redir(>>,cmd(a),cmd(out)),cmd(b))`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := parseAndPlanWithFixedSalt(t, tt.src, "")
			if err != nil {
				t.Fatalf("Plan failed: %v", err)
			}
			if len(plan.Steps) != 1 {
				t.Fatalf("Expected single step for %q, got %d", tt.src, len(plan.Steps))
			}

			got := treeShape(plan.Steps[0].Tree)
			if diff := cmp.Diff(tt.shape, got); diff != "" {
				t.Errorf("Tree shape mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// =============================================================================
// Blocked Tests (Skipped)
// =============================================================================

// TestParity_RedirectOperators tests > and >> operators
// BLOCKED: Redirect operators not implemented in IR builder
func TestParity_RedirectOperators(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantMode planfmt.RedirectMode
	}{
		{
			name:     "simple redirect overwrite",
			input:    `echo "hello" > output.txt`,
			wantMode: planfmt.RedirectOverwrite,
		},
		{
			name:     "simple redirect append",
			input:    `echo "world" >> output.txt`,
			wantMode: planfmt.RedirectAppend,
		},
		{
			name:     "redirect with variable",
			input:    `echo "data" > @var.OUTPUT_FILE`,
			wantMode: planfmt.RedirectOverwrite,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := parseAndPlan(t, tt.input, "")
			if err != nil {
				t.Fatalf("Plan failed: %v", err)
			}

			if len(plan.Steps) != 1 {
				t.Fatalf("Expected 1 step, got %d", len(plan.Steps))
			}

			step := plan.Steps[0]
			if step.Tree == nil {
				t.Fatal("Expected tree, got nil")
			}

			redirectNode, ok := step.Tree.(*planfmt.RedirectNode)
			if !ok {
				t.Fatalf("Expected RedirectNode, got %T", step.Tree)
			}

			if redirectNode.Mode != tt.wantMode {
				t.Errorf("Expected mode %v, got %v", tt.wantMode, redirectNode.Mode)
			}

			sourceCmd, ok := redirectNode.Source.(*planfmt.CommandNode)
			if !ok {
				t.Fatalf("Expected source to be CommandNode, got %T", redirectNode.Source)
			}
			if sourceCmd.Decorator != "@shell" {
				t.Errorf("Expected source decorator @shell, got %q", sourceCmd.Decorator)
			}

			if redirectNode.Target.Decorator != "@file" {
				t.Errorf("Expected target decorator @file, got %q", redirectNode.Target.Decorator)
			}
		})
	}
}

// TestParity_RedirectWithChaining tests redirects with operators
// BLOCKED: Redirect operators not implemented in IR builder
func TestParity_RedirectWithChaining(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantMode     planfmt.RedirectMode
		wantOperator string
	}{
		{
			name:         "redirect then AND",
			input:        `echo "a" > out.txt && echo "b"`,
			wantMode:     planfmt.RedirectOverwrite,
			wantOperator: "&&",
		},
		{
			name:         "redirect then OR",
			input:        `echo "a" > out.txt || echo "b"`,
			wantMode:     planfmt.RedirectOverwrite,
			wantOperator: "||",
		},
		{
			name:         "append then AND",
			input:        `echo "a" >> out.txt && echo "b"`,
			wantMode:     planfmt.RedirectAppend,
			wantOperator: "&&",
		},
		{
			name:         "redirect then PIPE",
			input:        `echo "a" > out.txt | cat`,
			wantMode:     planfmt.RedirectOverwrite,
			wantOperator: "|",
		},
		{
			name:         "redirect then SEMICOLON",
			input:        `echo "a" > out.txt; echo "b"`,
			wantMode:     planfmt.RedirectOverwrite,
			wantOperator: ";",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := parseAndPlan(t, tt.input, "")
			if err != nil {
				t.Fatalf("Plan failed: %v", err)
			}

			if len(plan.Steps) != 1 {
				t.Fatalf("Expected 1 step, got %d", len(plan.Steps))
			}

			step := plan.Steps[0]
			if step.Tree == nil {
				t.Fatal("Expected tree, got nil")
			}

			var leftNode, rightNode planfmt.ExecutionNode
			switch tt.wantOperator {
			case "&&":
				andNode, ok := step.Tree.(*planfmt.AndNode)
				if !ok {
					t.Fatalf("Expected AndNode for &&, got %T", step.Tree)
				}
				leftNode = andNode.Left
				rightNode = andNode.Right
			case "||":
				orNode, ok := step.Tree.(*planfmt.OrNode)
				if !ok {
					t.Fatalf("Expected OrNode for ||, got %T", step.Tree)
				}
				leftNode = orNode.Left
				rightNode = orNode.Right
			case "|":
				pipeNode, ok := step.Tree.(*planfmt.PipelineNode)
				if !ok {
					t.Fatalf("Expected PipelineNode for |, got %T", step.Tree)
				}
				if len(pipeNode.Commands) != 2 {
					t.Fatalf("Expected 2 pipeline commands, got %d", len(pipeNode.Commands))
				}
				redirectNode, ok := pipeNode.Commands[0].(*planfmt.RedirectNode)
				if !ok {
					t.Fatalf("Expected first pipeline command to be RedirectNode, got %T", pipeNode.Commands[0])
				}
				leftNode = redirectNode
				cmdNode, ok := pipeNode.Commands[1].(*planfmt.CommandNode)
				if !ok {
					t.Fatalf("Expected second pipeline command to be CommandNode, got %T", pipeNode.Commands[1])
				}
				rightNode = cmdNode
			case ";":
				seqNode, ok := step.Tree.(*planfmt.SequenceNode)
				if !ok {
					t.Fatalf("Expected SequenceNode for ;, got %T", step.Tree)
				}
				if len(seqNode.Nodes) != 2 {
					t.Fatalf("Expected 2 sequence nodes, got %d", len(seqNode.Nodes))
				}
				leftNode = seqNode.Nodes[0]
				rightNode = seqNode.Nodes[1]
			default:
				t.Fatalf("Unknown operator: %q", tt.wantOperator)
			}

			redirectNode, ok := leftNode.(*planfmt.RedirectNode)
			if !ok {
				t.Fatalf("Expected left side to be RedirectNode, got %T", leftNode)
			}

			if redirectNode.Mode != tt.wantMode {
				t.Errorf("Expected redirect mode %v, got %v", tt.wantMode, redirectNode.Mode)
			}

			rightCmd, ok := rightNode.(*planfmt.CommandNode)
			if !ok {
				t.Fatalf("Expected right side to be CommandNode, got %T", rightNode)
			}
			if rightCmd.Decorator != "@shell" {
				t.Errorf("Expected right decorator @shell, got %q", rightCmd.Decorator)
			}
		})
	}
}
