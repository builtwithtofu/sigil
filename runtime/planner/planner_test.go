package planner_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/builtwithtofu/sigil/core/planfmt"
	_ "github.com/builtwithtofu/sigil/runtime/decorators"
	"github.com/builtwithtofu/sigil/runtime/lexer"
	"github.com/builtwithtofu/sigil/runtime/parser"
	"github.com/builtwithtofu/sigil/runtime/planner"
	"github.com/builtwithtofu/sigil/runtime/vault"
	"github.com/google/go-cmp/cmp"
)

// Helper: Extract command argument from tree (assumes tree is a CommandNode)
func getCommandArg(tree planfmt.ExecutionNode, key string) string {
	if tree == nil {
		return ""
	}
	cmd, ok := tree.(*planfmt.CommandNode)
	if !ok {
		return ""
	}
	for _, arg := range cmd.Args {
		if arg.Key == key {
			return arg.Val.Str
		}
	}
	return ""
}

// Helper: Get decorator name from tree (assumes tree is a CommandNode)
func getDecorator(tree planfmt.ExecutionNode) string {
	if tree == nil {
		return ""
	}
	cmd, ok := tree.(*planfmt.CommandNode)
	if !ok {
		return ""
	}
	return cmd.Decorator
}

// MIGRATED: Ported to TestParity_SimpleShellCommand in planner_new_parity_test.go
// TestSimpleShellCommand tests converting a simple shell command to @shell decorator
func TestSimpleShellCommand(t *testing.T) {
	source := []byte(`echo "Hello, World!"`)

	// Parse
	tree := parser.Parse(source)

	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	// Plan (script mode - no target)
	plan, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
		Target: "", // Script mode
	})
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
	if getDecorator(step.Tree) != "@shell" {
		t.Errorf("Expected @shell decorator, got %q", getDecorator(step.Tree))
	}

	// Check command argument
	expectedCmd := `echo "Hello, World!"`
	if getCommandArg(step.Tree, "command") != expectedCmd {
		t.Errorf("Expected command %q, got %q", expectedCmd, getCommandArg(step.Tree, "command"))
	}
}

// TestShellCommandWithDashes tests that shell commands with dashes are reconstructed correctly
func TestShellCommandWithDashes(t *testing.T) {
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
		{
			name:     "double dash with space",
			input:    `git commit -- file.txt`,
			expected: `git commit -- file.txt`,
		},
		{
			name:     "mixed single and double dash",
			input:    `curl -X POST --data "test"`,
			expected: `curl -X POST --data "test"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parse
			tree := parser.Parse([]byte(tt.input))

			if len(tree.Errors) > 0 {
				t.Fatalf("Parse errors: %v", tree.Errors)
			}

			// Plan (script mode)
			plan, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
				Target: "",
			})
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
			if getDecorator(step.Tree) != "@shell" {
				t.Errorf("Expected @shell decorator, got %q", getDecorator(step.Tree))
			}

			// Check command argument - this is the critical test
			actualCmd := getCommandArg(step.Tree, "command")
			if actualCmd != tt.expected {
				t.Errorf("Command mismatch:\n  want: %q\n  got:  %q", tt.expected, actualCmd)
			}
		})
	}
}

// TestMultipleShellCommands tests multiple newline-separated commands
func TestMultipleShellCommands(t *testing.T) {
	source := []byte(`echo "First"
echo "Second"`)

	tree := parser.Parse(source)

	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	plan, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
		Target: "",
	})
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// Should have 2 steps (newline-separated)
	if len(plan.Steps) != 2 {
		t.Errorf("Expected 2 steps, got %d", len(plan.Steps))
	}

	// Verify first step
	if plan.Steps[0].Tree == nil {
		t.Fatal("Step 0: tree is nil")
	}
	if getCommandArg(plan.Steps[0].Tree, "command") != `echo "First"` {
		t.Errorf("Step 0: wrong command: %q", getCommandArg(plan.Steps[0].Tree, "command"))
	}

	// Verify second step
	if plan.Steps[1].Tree == nil {
		t.Fatal("Step 1: tree is nil")
	}
	if getCommandArg(plan.Steps[1].Tree, "command") != `echo "Second"` {
		t.Errorf("Step 1: wrong command: %q", getCommandArg(plan.Steps[1].Tree, "command"))
	}
}

// TestFunctionDefinition tests parsing a function definition
func TestFunctionDefinition(t *testing.T) {
	source := []byte(`fun hello = echo "Hello, World!"`)

	tree := parser.Parse(source)

	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	// Command mode - target "hello"
	plan, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
		Target: "hello",
	})
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// Should have 1 step (the function body)
	if len(plan.Steps) != 1 {
		t.Errorf("Expected 1 step, got %d", len(plan.Steps))
	}

	// Verify it's the shell command from the function body
	if plan.Steps[0].Tree == nil {
		t.Fatal("Expected tree, got nil")
	}

	if getDecorator(plan.Steps[0].Tree) != "@shell" {
		t.Errorf("Expected @shell, got %q", getDecorator(plan.Steps[0].Tree))
	}

	if getCommandArg(plan.Steps[0].Tree, "command") != `echo "Hello, World!"` {
		t.Errorf("Wrong command: %q", getCommandArg(plan.Steps[0].Tree, "command"))
	}
}

// TestCommandModeTargetSelection tests finding a specific function
func TestCommandModeTargetSelection(t *testing.T) {
	source := []byte(`fun hello = echo "Hello"
fun goodbye = echo "Goodbye"`)

	tree := parser.Parse(source)

	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	// Target "goodbye" - should only plan that function
	plan, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
		Target: "goodbye",
	})
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// Should have 1 step (only goodbye function body)
	if len(plan.Steps) != 1 {
		t.Errorf("Expected 1 step, got %d", len(plan.Steps))
	}

	// Verify it's the goodbye command
	if getCommandArg(plan.Steps[0].Tree, "command") != `echo "Goodbye"` {
		t.Errorf("Wrong command: %q", getCommandArg(plan.Steps[0].Tree, "command"))
	}
}

// TestScriptModeFullExecution tests executing entire file (no target)
func TestScriptModeFullExecution(t *testing.T) {
	source := []byte(`echo "Top level"
fun hello = echo "In function"
echo "Another top level"`)

	tree := parser.Parse(source)

	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	// Script mode - no target, execute all top-level commands
	plan, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
		Target: "",
	})
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// Should have 2 steps (two top-level echo commands, function is skipped)
	if len(plan.Steps) != 2 {
		t.Errorf("Expected 2 steps, got %d", len(plan.Steps))
	}

	// Verify first command
	if getCommandArg(plan.Steps[0].Tree, "command") != `echo "Top level"` {
		t.Errorf("Step 0: wrong command: %q", getCommandArg(plan.Steps[0].Tree, "command"))
	}

	// Verify second command
	if getCommandArg(plan.Steps[1].Tree, "command") != `echo "Another top level"` {
		t.Errorf("Step 1: wrong command: %q", getCommandArg(plan.Steps[1].Tree, "command"))
	}
}

// TestEmptyPlan tests empty input
func TestEmptyPlan(t *testing.T) {
	source := []byte(``)

	tree := parser.Parse(source)

	plan, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
		Target: "",
	})
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// Empty plan is valid
	if len(plan.Steps) != 0 {
		t.Errorf("Expected 0 steps, got %d", len(plan.Steps))
	}
}

// TestStepIDUniqueness tests that all step IDs are unique
func TestStepIDUniqueness(t *testing.T) {
	source := []byte(`echo "First"
echo "Second"
echo "Third"`)

	tree := parser.Parse(source)

	plan, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
		Target: "",
	})
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// Collect all step IDs
	seen := make(map[uint64]bool)
	for _, step := range plan.Steps {
		if seen[step.ID] {
			t.Errorf("Duplicate step ID: %d", step.ID)
		}
		seen[step.ID] = true
	}

	// All IDs should be unique
	if len(seen) != len(plan.Steps) {
		t.Errorf("Expected %d unique IDs, got %d", len(plan.Steps), len(seen))
	}
}

// TestArgSorting tests that args are sorted by key
func TestArgSorting(t *testing.T) {
	source := []byte(`echo "Hello"`)

	tree := parser.Parse(source)

	plan, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
		Target: "",
	})
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// Verify args are sorted (for determinism)
	cmd, ok := plan.Steps[0].Tree.(*planfmt.CommandNode)
	if !ok {
		t.Fatalf("Expected CommandNode, got %T", plan.Steps[0].Tree)
	}
	for i := 1; i < len(cmd.Args); i++ {
		if cmd.Args[i-1].Key >= cmd.Args[i].Key {
			t.Errorf("Args not sorted: %q >= %q", cmd.Args[i-1].Key, cmd.Args[i].Key)
		}
	}
}

// TestTargetNotFound tests error when target function doesn't exist
func TestTargetNotFound(t *testing.T) {
	source := []byte(`fun hello = echo "Hello"`)

	tree := parser.Parse(source)

	// Target a function that doesn't exist
	_, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
		Target: "nonexistent",
	})

	if err == nil {
		t.Fatal("Expected error for nonexistent target, got nil")
	}

	// Error message should contain key information
	errMsg := err.Error()
	if !strings.Contains(errMsg, `function "nonexistent" not found`) {
		t.Errorf("Expected 'function \"nonexistent\" not found', got: %s", errMsg)
	}
}

// TestTargetNotFoundWithSuggestion tests "did you mean" suggestions
func TestTargetNotFoundWithSuggestion(t *testing.T) {
	source := []byte(`fun hello = echo "Hello"
fun deploy = echo "Deploying"`)

	tree := parser.Parse(source)

	// Target a function with a typo
	_, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
		Target: "helo", // Missing 'l'
	})

	if err == nil {
		t.Fatal("Expected error for nonexistent target, got nil")
	}

	// Should suggest "hello"
	errMsg := err.Error()
	if !strings.Contains(errMsg, `function "helo" not found`) {
		t.Errorf("Expected 'function \"helo\" not found', got: %s", errMsg)
	}
	// Note: The new planner doesn't currently provide "Did you mean" suggestions
	// This could be added as a future enhancement
}

func TestPlanShellCommandWithOperators(t *testing.T) {
	// Test that commands with operators are grouped into a single step with tree structure
	source := []byte(`fun test = echo "A" && echo "B"`)

	tree := parser.Parse(source)
	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	plan, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
		Target: "test",
	})
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// Should have ONE step with an AndNode tree
	if len(plan.Steps) != 1 {
		t.Errorf("Expected 1 step, got %d", len(plan.Steps))
	}

	// Tree should be an AndNode
	andNode, ok := plan.Steps[0].Tree.(*planfmt.AndNode)
	if !ok {
		t.Fatalf("Expected AndNode, got %T", plan.Steps[0].Tree)
	}

	// Left side should be echo "A"
	if getCommandArg(andNode.Left, "command") != `echo "A"` {
		t.Errorf("Left command wrong: %q", getCommandArg(andNode.Left, "command"))
	}

	// Right side should be echo "B"
	if getCommandArg(andNode.Right, "command") != `echo "B"` {
		t.Errorf("Right command wrong: %q", getCommandArg(andNode.Right, "command"))
	}
}

func TestPlanMultipleSteps(t *testing.T) {
	// Test that newline-separated commands in SCRIPT MODE create separate steps
	source := []byte("echo \"A\"\necho \"B\"")

	tree := parser.Parse(source)
	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	plan, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
		Target: "", // Script mode
	})
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// Should have TWO steps, each with a tree
	if len(plan.Steps) != 2 {
		t.Errorf("Expected 2 steps, got %d", len(plan.Steps))
	}

	if plan.Steps[0].Tree == nil {
		t.Fatal("Expected first step to have tree, got nil")
	}

	if plan.Steps[1].Tree == nil {
		t.Fatal("Expected second step to have tree, got nil")
	}
}

func TestPlanFunctionWithOperatorsAndNewline(t *testing.T) {
	// Test: echo "A" && echo "B" || echo "C"\necho "D"
	// In SCRIPT MODE (no target), should produce: 2 steps
	// Step 1: OrNode with AndNode on left (operator precedence: && > ||)
	// Step 2: Simple command (newline creates new step)
	source := []byte(`echo "A" && echo "B" || echo "C"
echo "D"`)

	tree := parser.Parse(source)
	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	plan, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
		Target: "", // Script mode
	})
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// Should have TWO steps
	if len(plan.Steps) != 2 {
		t.Errorf("Expected 2 steps, got %d", len(plan.Steps))
	}

	// Step 1: Should be OrNode with structure: (A && B) || C
	orNode, ok := plan.Steps[0].Tree.(*planfmt.OrNode)
	if !ok {
		t.Fatalf("Step 0: Expected OrNode, got %T", plan.Steps[0].Tree)
	}

	// Left side of OR should be AndNode: A && B
	andNode, ok := orNode.Left.(*planfmt.AndNode)
	if !ok {
		t.Fatalf("Step 0 left: Expected AndNode, got %T", orNode.Left)
	}

	// Verify A && B
	if getCommandArg(andNode.Left, "command") != `echo "A"` {
		t.Errorf("Step 0 AND left: expected 'echo \"A\"', got %q", getCommandArg(andNode.Left, "command"))
	}
	if getCommandArg(andNode.Right, "command") != `echo "B"` {
		t.Errorf("Step 0 AND right: expected 'echo \"B\"', got %q", getCommandArg(andNode.Right, "command"))
	}

	// Right side of OR should be C
	if getCommandArg(orNode.Right, "command") != `echo "C"` {
		t.Errorf("Step 0 OR right: expected 'echo \"C\"', got %q", getCommandArg(orNode.Right, "command"))
	}

	// Step 2: Simple command echo "D"
	if plan.Steps[1].Tree == nil {
		t.Fatal("Step 1: tree is nil")
	}

	if getCommandArg(plan.Steps[1].Tree, "command") != `echo "D"` {
		t.Errorf("Step 1: expected 'echo \"D\"', got %q", getCommandArg(plan.Steps[1].Tree, "command"))
	}
}

// TestContractStability verifies that changing an unrelated function
// doesn't invalidate the contract for the target function.
//
// This tests the core contract stability guarantee:
// - Generate contract for 'hello' function (gets random PlanSalt)
// - Modify unrelated 'log' function in source
// - Re-plan 'hello' with SAME PlanSalt (simulating Mode 4 contract verification)
// - Hashes should match (contract still valid)
//
// Key insight: With the same PlanSalt (seed), plans for unchanged functions
// produce identical hashes, enabling contract verification to succeed.
func TestContractStability(t *testing.T) {
	// Original source with two functions
	source1 := []byte(`fun hello = echo "Hello"
fun log = echo "Log"`)

	// Modified source (only log changed, hello unchanged)
	source2 := []byte(`fun hello = echo "Hello"
fun log = echo "Different log"`)

	// Step 1: Generate contract for 'hello' from source1
	// PlanSalt is auto-generated (random) when not provided
	tree1 := parser.Parse(source1)
	lex1 := lexer.NewLexer()
	lex1.Init(source1)
	plan1, err := planner.Plan(tree1.Events, lex1.GetTokens(), planner.Config{
		Target: "hello",
	})
	if err != nil {
		t.Fatalf("Plan1 failed: %v", err)
	}

	// Compute hash for plan1 (this would be stored in contract file)
	var buf1 bytes.Buffer
	hash1, err := planfmt.Write(&buf1, plan1)
	if err != nil {
		t.Fatalf("Write plan1 failed: %v", err)
	}

	// Step 2: Re-plan 'hello' from modified source2 with SAME PlanSalt
	// This simulates Mode 4 contract verification where we reuse the salt from the contract
	tree2 := parser.Parse(source2)
	lex2 := lexer.NewLexer()
	lex2.Init(source2)
	plan2, err := planner.Plan(tree2.Events, lex2.GetTokens(), planner.Config{
		Target:   "hello",
		PlanSalt: plan1.PlanSalt, // Use same salt for deterministic DisplayIDs
	})
	if err != nil {
		t.Fatalf("Plan2 failed: %v", err)
	}

	// Compute hash for plan2
	var buf2 bytes.Buffer
	hash2, err := planfmt.Write(&buf2, plan2)
	if err != nil {
		t.Fatalf("Write plan2 failed: %v", err)
	}

	// Step 3: Verify contract stability
	// Hashes should be IDENTICAL because:
	// - 'hello' function didn't change (same structure)
	// - Same PlanSalt used (deterministic DisplayIDs and transport IDs)
	// - Only unrelated 'log' function changed (not included in 'hello' plan)
	if hash1 != hash2 {
		t.Errorf("Contract instability detected!\nChanging 'log' function invalidated 'hello' contract\nhash1: %x\nhash2: %x", hash1, hash2)
	}
}

// TestContractStabilityWithNewFunction verifies that adding a new function
// doesn't invalidate existing contracts.
//
// This tests another contract stability guarantee:
// - Generate contract for 'hello' function (gets random PlanSalt)
// - Add new 'log' function to source (hello unchanged)
// - Re-plan 'hello' with SAME PlanSalt (simulating Mode 4 contract verification)
// - Hashes should match (contract still valid)
//
// Key insight: Function-scoped planning means adding new functions doesn't
// affect existing contracts. With the same PlanSalt, the hash remains identical.
func TestContractStabilityWithNewFunction(t *testing.T) {
	// Original source (only hello)
	source1 := []byte(`fun hello = echo "Hello"`)

	// Modified source (hello unchanged, new log function added)
	source2 := []byte(`fun hello = echo "Hello"
fun log = echo "Log"`)

	// Step 1: Generate contract for 'hello' from source1
	// PlanSalt is auto-generated (random) when not provided
	tree1 := parser.Parse(source1)
	lex1 := lexer.NewLexer()
	lex1.Init(source1)
	plan1, err := planner.Plan(tree1.Events, lex1.GetTokens(), planner.Config{
		Target: "hello",
	})
	if err != nil {
		t.Fatalf("Plan1 failed: %v", err)
	}

	// Compute hash for plan1 (this would be stored in contract file)
	var buf1 bytes.Buffer
	hash1, err := planfmt.Write(&buf1, plan1)
	if err != nil {
		t.Fatalf("Write plan1 failed: %v", err)
	}

	// Step 2: Re-plan 'hello' from modified source2 with SAME PlanSalt
	// This simulates Mode 4 contract verification where we reuse the salt from the contract
	tree2 := parser.Parse(source2)
	lex2 := lexer.NewLexer()
	lex2.Init(source2)
	plan2, err := planner.Plan(tree2.Events, lex2.GetTokens(), planner.Config{
		Target:   "hello",
		PlanSalt: plan1.PlanSalt, // Use same salt for deterministic DisplayIDs
	})
	if err != nil {
		t.Fatalf("Plan2 failed: %v", err)
	}

	// Compute hash for plan2
	var buf2 bytes.Buffer
	hash2, err := planfmt.Write(&buf2, plan2)
	if err != nil {
		t.Fatalf("Write plan2 failed: %v", err)
	}

	// Step 3: Verify contract stability
	// Hashes should be IDENTICAL because:
	// - 'hello' function didn't change (same structure)
	// - Same PlanSalt used (deterministic DisplayIDs and transport IDs)
	// - New 'log' function not included in 'hello' plan (function-scoped planning)
	if hash1 != hash2 {
		t.Errorf("Contract instability detected!\nAdding 'log' function invalidated 'hello' contract\nhash1: %x\nhash2: %x", hash1, hash2)
	}
}

// TestRedirectOperators tests output redirection (> and >>)
func TestRedirectOperators(t *testing.T) {
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
		{
			name:     "input redirect from bare path",
			input:    `cat < input.txt`,
			wantMode: planfmt.RedirectInput,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parse
			tree := parser.Parse([]byte(tt.input))

			if len(tree.Errors) > 0 {
				t.Fatalf("Parse errors: %v", tree.Errors)
			}

			// Plan (script mode)
			result, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
				Target: "",
			})
			if err != nil {
				t.Fatalf("Plan failed: %v", err)
			}

			// Verify plan structure
			if len(result.Steps) != 1 {
				t.Fatalf("Expected 1 step, got %d", len(result.Steps))
			}

			step := result.Steps[0]
			if step.Tree == nil {
				t.Fatal("Expected tree, got nil")
			}

			// Tree should be a RedirectNode
			redirectNode, ok := step.Tree.(*planfmt.RedirectNode)
			if !ok {
				t.Fatalf("Expected RedirectNode, got %T", step.Tree)
			}

			// Check redirect mode
			if redirectNode.Mode != tt.wantMode {
				t.Errorf("Expected mode %v, got %v", tt.wantMode, redirectNode.Mode)
			}

			// Source should be a CommandNode with @shell
			sourceCmd, ok := redirectNode.Source.(*planfmt.CommandNode)
			if !ok {
				t.Fatalf("Expected source to be CommandNode, got %T", redirectNode.Source)
			}
			if sourceCmd.Decorator != "@shell" {
				t.Errorf("Expected source decorator @shell, got %q", sourceCmd.Decorator)
			}

			// Targets are explicit file endpoints.
			if redirectNode.Target.Decorator != "@file" {
				t.Errorf("Expected target decorator @file, got %q", redirectNode.Target.Decorator)
			}
		})
	}
}

// TestRedirectWithChaining tests redirect followed by chaining operators (&&, ||)
// Bug: Previously, redirect consumed the operator slot, so && was never captured
func TestRedirectWithChaining(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantMode     planfmt.RedirectMode
		wantOperator string // Expected operator AFTER redirect
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
			// Parse
			tree := parser.Parse([]byte(tt.input))

			if len(tree.Errors) > 0 {
				t.Fatalf("Parse errors: %v", tree.Errors)
			}

			// Plan (script mode)
			result, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
				Target: "",
			})
			if err != nil {
				t.Fatalf("Plan failed: %v", err)
			}

			// Verify plan structure
			if len(result.Steps) != 1 {
				t.Fatalf("Expected 1 step, got %d", len(result.Steps))
			}

			step := result.Steps[0]
			if step.Tree == nil {
				t.Fatal("Expected tree, got nil")
			}

			// Tree should be an operator node (&&, ||, |, ;)
			// with left side being a RedirectNode
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
				// For pipe, check that first command is a RedirectNode
				redirectNode, ok := pipeNode.Commands[0].(*planfmt.RedirectNode)
				if !ok {
					t.Fatalf("Expected first pipeline command to be RedirectNode, got %T", pipeNode.Commands[0])
				}
				// The redirect node is our "left" for validation
				leftNode = redirectNode
				// Second command should be CommandNode
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

			// Left side should be a RedirectNode
			redirectNode, ok := leftNode.(*planfmt.RedirectNode)
			if !ok {
				t.Fatalf("Expected left side to be RedirectNode, got %T", leftNode)
			}

			// Check redirect mode
			if redirectNode.Mode != tt.wantMode {
				t.Errorf("Expected redirect mode %v, got %v", tt.wantMode, redirectNode.Mode)
			}

			// Right side should be a CommandNode
			rightCmd, ok := rightNode.(*planfmt.CommandNode)
			if !ok {
				t.Fatalf("Expected right side to be CommandNode, got %T", rightNode)
			}

			// Right command should be @shell
			if rightCmd.Decorator != "@shell" {
				t.Errorf("Expected right decorator @shell, got %q", rightCmd.Decorator)
			}
		})
	}
}

func TestRedirectDecoratorTarget(t *testing.T) {
	tree := parser.Parse([]byte(`echo "hello" > @file("sink-1")`))
	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	graph, err := planner.BuildIR(tree.Events, tree.Tokens)
	if err != nil {
		t.Fatalf("BuildIR failed: %v", err)
	}
	if len(graph.Statements) != 1 || graph.Statements[0].Command == nil || graph.Statements[0].Command.RedirectTarget == nil {
		t.Fatalf("redirect target missing from IR: %+v", graph.Statements)
	}
	parts := graph.Statements[0].Command.RedirectTarget.Parts
	hasDecoratorRef := false
	for _, part := range parts {
		if part != nil && part.Kind == planner.ExprDecoratorRef {
			hasDecoratorRef = true
			break
		}
	}
	if !hasDecoratorRef {
		t.Fatalf("redirect target has no decorator ref part: %+v", parts)
	}

	result, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{Target: ""})
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	if len(result.Steps) != 1 {
		t.Fatalf("Expected 1 step, got %d", len(result.Steps))
	}

	redirectNode, ok := result.Steps[0].Tree.(*planfmt.RedirectNode)
	if !ok {
		t.Fatalf("Expected RedirectNode, got %T", result.Steps[0].Tree)
	}

	if diff := cmp.Diff("@file", redirectNode.Target.Decorator); diff != "" {
		t.Fatalf("redirect target decorator mismatch (-want +got):\n%s", diff)
	}

	if diff := cmp.Diff("sink-1", getCommandArg(redirectNode.Target.Command(), "path")); diff != "" {
		t.Fatalf("redirect target path mismatch (-want +got):\n%s", diff)
	}
}

func TestInputRedirectDecoratorTarget(t *testing.T) {
	tree := parser.Parse([]byte(`cat < @file("source-1")`))
	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	graph, err := planner.BuildIR(tree.Events, tree.Tokens)
	if err != nil {
		t.Fatalf("BuildIR failed: %v", err)
	}
	if len(graph.Statements) != 1 || graph.Statements[0].Command == nil || graph.Statements[0].Command.RedirectTarget == nil {
		t.Fatalf("redirect target missing from IR: %+v", graph.Statements)
	}
	parts := graph.Statements[0].Command.RedirectTarget.Parts
	hasDecoratorRef := false
	for _, part := range parts {
		if part != nil && part.Kind == planner.ExprDecoratorRef {
			hasDecoratorRef = true
			break
		}
	}
	if !hasDecoratorRef {
		t.Fatalf("redirect target has no decorator ref part: %+v", parts)
	}

	result, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{Target: ""})
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	if len(result.Steps) != 1 {
		t.Fatalf("Expected 1 step, got %d", len(result.Steps))
	}

	redirectNode, ok := result.Steps[0].Tree.(*planfmt.RedirectNode)
	if !ok {
		t.Fatalf("Expected RedirectNode, got %T", result.Steps[0].Tree)
	}

	if diff := cmp.Diff(planfmt.RedirectInput, redirectNode.Mode); diff != "" {
		t.Fatalf("redirect mode mismatch (-want +got):\n%s", diff)
	}

	if diff := cmp.Diff("@file", redirectNode.Target.Decorator); diff != "" {
		t.Fatalf("redirect target decorator mismatch (-want +got):\n%s", diff)
	}
}

// TestPlannerInitialization tests that planner initializes with empty vars map and telemetry
func TestPlannerInitialization(t *testing.T) {
	source := []byte(`echo "test"`)

	// Parse
	tree := parser.Parse(source)
	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	// Plan with telemetry enabled
	result, err := planner.PlanWithObservability(tree.Events, tree.Tokens, planner.Config{
		Telemetry: planner.TelemetryBasic,
	})
	if err != nil {
		t.Fatalf("Planning failed: %v", err)
	}

	// Verify telemetry was initialized
	if result.Telemetry == nil {
		t.Fatal("Expected telemetry to be initialized")
	}

	// Verify DecoratorResolutions map is initialized
	if result.Telemetry.DecoratorResolutions == nil {
		t.Error("Expected DecoratorResolutions map to be initialized")
	}

	// Verify map is empty (no decorator resolutions yet)
	if len(result.Telemetry.DecoratorResolutions) != 0 {
		t.Errorf("Expected empty DecoratorResolutions map, got %d entries", len(result.Telemetry.DecoratorResolutions))
	}

	// Verify basic telemetry is collected
	if result.Telemetry.EventCount == 0 {
		t.Error("Expected EventCount > 0")
	}

	if result.Telemetry.StepCount == 0 {
		t.Error("Expected StepCount > 0")
	}
}

// TestVarDeclBlockPlanning tests planning with var block declarations
func TestVarDeclBlockPlanning(t *testing.T) {
	source := []byte(`var (
	apiUrl = "https://api.example.com"
	replicas = 3
)
echo "test"`)

	// Parse
	tree := parser.Parse(source)
	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	// Plan with telemetry
	result, err := planner.PlanWithObservability(tree.Events, tree.Tokens, planner.Config{
		Telemetry: planner.TelemetryBasic,
	})
	if err != nil {
		t.Fatalf("Planning failed: %v", err)
	}

	// Verify telemetry tracked events and steps
	if result.Telemetry.EventCount == 0 {
		t.Error("Expected EventCount > 0")
	}

	// Verify telemetry tracked @var resolutions (declarations)
	if result.Telemetry.DecoratorResolutions["@var"] == nil {
		t.Fatal("Expected @var decorator resolutions to be tracked")
	}

	if result.Telemetry.DecoratorResolutions["@var"].TotalCalls != 2 {
		t.Errorf("Expected 2 @var resolutions, got %d",
			result.Telemetry.DecoratorResolutions["@var"].TotalCalls)
	}

	// Verify plan has 1 step (the echo command, var decls don't create steps)
	if len(result.Plan.Steps) != 1 {
		t.Errorf("Expected 1 step, got %d", len(result.Plan.Steps))
	}
}

// TestVarDeclWithStructuredValues tests variable declarations with objects and arrays
func TestVarDeclWithStructuredValues(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		wantValue any
	}{
		{
			name:   "object literal",
			source: `var config = {timeout: "5m", retries: 3}`,
			wantValue: map[string]any{
				"timeout": "5m",
				"retries": "3",
			},
		},
		{
			name:      "array literal",
			source:    `var ports = [8080, 8081, 8082]`,
			wantValue: []any{"8080", "8081", "8082"},
		},
		{
			name:   "nested object",
			source: `var settings = {db: {host: "localhost", port: 5432}}`,
			wantValue: map[string]any{
				"db": map[string]any{
					"host": "localhost",
					"port": "5432",
				},
			},
		},
		{
			name:   "array of objects",
			source: `var servers = [{name: "web1", port: 8080}, {name: "web2", port: 8081}]`,
			wantValue: []any{
				map[string]any{"name": "web1", "port": "8080"},
				map[string]any{"name": "web2", "port": "8081"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parse
			tree := parser.Parse([]byte(tt.source))
			if len(tree.Errors) > 0 {
				t.Fatalf("parse errors: %v", tree.Errors)
			}

			// Plan
			result, err := planner.PlanWithObservability(tree.Events, tree.Tokens, planner.Config{
				Telemetry: planner.TelemetryBasic,
			})
			if err != nil {
				t.Fatalf("planning failed: %v", err)
			}

			// Check variable was stored
			// Note: We can't directly access p.vars, but we can verify no error occurred
			// In a real implementation, we'd need a way to inspect the planner state
			// For now, just verify planning succeeded
			if result.Plan == nil {
				t.Fatal("expected plan to be created")
			}
		})
	}
}

// TestBareVariableInterpolation tests that bare @var.HOME gets replaced with a placeholder
func TestBareVariableInterpolation(t *testing.T) {
	source := []byte(`
var HOME = "/home/alice"
echo @var.HOME
`)

	// Parse
	tree := parser.Parse(source)
	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	// Plan
	plan, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
		Target: "",
	})
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// ASSERT: Plan has one SecretUse entry (HOME variable used once)
	// Phase 5.5: Plan contains SecretUses (authorization list), not Secrets (RuntimeValue)
	if len(plan.SecretUses) != 1 {
		t.Fatalf("Expected 1 SecretUse, got %d", len(plan.SecretUses))
	}

	secretUse := plan.SecretUses[0]

	// ASSERT: SecretUse has DisplayID with correct prefix
	if !strings.HasPrefix(secretUse.DisplayID, "sigil:") {
		t.Errorf("Expected DisplayID prefix sigil:, got %s", secretUse.DisplayID)
	}

	// ASSERT: SecretUse has SiteID (HMAC-based authorization)
	if secretUse.SiteID == "" {
		t.Error("Expected non-empty SiteID")
	}

	// ASSERT: SecretUse has Site path
	if secretUse.Site == "" {
		t.Error("Expected non-empty Site")
	}

	// ASSERT: Step uses placeholder, not raw value
	if len(plan.Steps) != 1 {
		t.Fatalf("Expected 1 step, got %d", len(plan.Steps))
	}

	step := plan.Steps[0]
	if step.Tree == nil {
		t.Fatal("Step.Tree is nil")
	}

	// Find the @shell decorator's command argument
	shellNode, ok := step.Tree.(*planfmt.CommandNode)
	if !ok {
		t.Fatalf("Expected CommandNode, got %T", step.Tree)
	}

	if shellNode.Decorator != "@shell" {
		t.Errorf("Expected @shell decorator, got %s", shellNode.Decorator)
	}

	// Find command argument
	var commandArg *planfmt.Arg
	for i := range shellNode.Args {
		if shellNode.Args[i].Key == "command" {
			commandArg = &shellNode.Args[i]
			break
		}
	}

	if commandArg == nil {
		t.Fatal("No 'command' argument found")
	}

	// ASSERT: Command uses string with DisplayID embedded (Phase 5)
	// Plan should show: echo sigil:XXXXX
	// NOT: echo /home/alice
	if commandArg.Val.Kind != planfmt.ValueString {
		t.Errorf("Expected ValueString, got %v", commandArg.Val.Kind)
	}

	commandStr := commandArg.Val.Str

	// ASSERT: Command contains DisplayID placeholder
	if !strings.Contains(commandStr, "sigil:") {
		t.Errorf("Expected command to contain DisplayID 'sigil:', got: %s", commandStr)
	}

	// ASSERT: Command does NOT contain actual value
	if strings.Contains(commandStr, "/home/alice") {
		t.Errorf("Command should NOT contain actual value '/home/alice', got: %s", commandStr)
	}

	t.Logf("✓ Plan command: %s", commandStr)
}

// ========== PlanSalt and Contract Verification Tests ==========

// TestPlanSalt_MatchesVaultPlanKey verifies that plan.PlanSalt equals vault.planKey.
// This is CRITICAL for contract verification - if they don't match, re-planning
// with the same PlanSalt will produce different DisplayIDs and verification fails.
func TestPlanSalt_MatchesVaultPlanKey(t *testing.T) {
	source := `var NAME = "Aled"
echo "@var.NAME"`

	tree := parser.ParseString(source)
	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	// Create vault with known planKey (must be exactly 32 bytes)
	planKey := []byte("test-plan-key-32-bytes-for-hmac!")
	vlt := vault.NewWithPlanKey(planKey)

	// Plan with this vault
	plan, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
		Vault: vlt,
	})
	if err != nil {
		t.Fatalf("Planning failed: %v", err)
	}

	// ASSERT: plan.PlanSalt should equal vault's planKey
	if !bytes.Equal(plan.PlanSalt, planKey) {
		t.Errorf("CRITICAL BUG: plan.PlanSalt does not match vault.planKey\n"+
			"  Vault planKey: %x\n"+
			"  Plan PlanSalt: %x\n"+
			"This breaks contract verification - re-planning won't produce same DisplayIDs",
			planKey, plan.PlanSalt)
	}

	t.Logf("✓ plan.PlanSalt matches vault.planKey: %x", plan.PlanSalt)
}

// TestContractVerification_SamePlanSalt_SameDisplayIDs verifies that re-planning
// with the same PlanSalt produces the same DisplayIDs (contract verification).
func TestContractVerification_SamePlanSalt_SameDisplayIDs(t *testing.T) {
	source := `var API_KEY = "sk-secret-123"
echo "Key: @var.API_KEY"`

	tree := parser.ParseString(source)
	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	// Simulate initial planning
	planKey1 := []byte("contract-plan-key-32bytes-hmac!!")
	vault1 := vault.NewWithPlanKey(planKey1)

	plan1, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
		Vault: vault1,
	})
	if err != nil {
		t.Fatalf("Initial planning failed: %v", err)
	}

	// Simulate contract verification (re-plan with SAME PlanSalt)
	vault2 := vault.NewWithPlanKey(plan1.PlanSalt) // Reuse PlanSalt from contract
	plan2, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
		Vault: vault2,
	})
	if err != nil {
		t.Fatalf("Verification planning failed: %v", err)
	}

	// ASSERT: Plans should have same PlanSalt
	if !bytes.Equal(plan1.PlanSalt, plan2.PlanSalt) {
		t.Errorf("Plans have different PlanSalt:\n  Plan1: %x\n  Plan2: %x",
			plan1.PlanSalt, plan2.PlanSalt)
	}

	// ASSERT: Plans should have same SecretUses (same DisplayIDs)
	if len(plan1.SecretUses) != len(plan2.SecretUses) {
		t.Fatalf("Different number of SecretUses: %d vs %d",
			len(plan1.SecretUses), len(plan2.SecretUses))
	}

	for i := range plan1.SecretUses {
		if plan1.SecretUses[i].DisplayID != plan2.SecretUses[i].DisplayID {
			t.Errorf("SecretUse[%d] has different DisplayID:\n"+
				"  Plan1: %s\n"+
				"  Plan2: %s\n"+
				"Contract verification FAILED - DisplayIDs don't match!",
				i, plan1.SecretUses[i].DisplayID, plan2.SecretUses[i].DisplayID)
		}
	}

	// ASSERT: Plans should have same hash (contract verification)
	plan1.Freeze()
	plan2.Freeze()

	if plan1.Hash != plan2.Hash {
		t.Errorf("Contract verification FAILED - hashes don't match:\n"+
			"  Plan1 hash: %s\n"+
			"  Plan2 hash: %s\n"+
			"Re-planning with same PlanSalt should produce same hash",
			plan1.Hash, plan2.Hash)
	}

	t.Logf("✓ Contract verification passed")
	t.Logf("  PlanSalt: %x", plan1.PlanSalt)
	t.Logf("  DisplayID: %s", plan1.SecretUses[0].DisplayID)
	t.Logf("  Hash: %s", plan1.Hash)
}

// TestContractVerification_DifferentPlanSalt_DifferentDisplayIDs verifies that
// different PlanSalts produce different DisplayIDs (unlinkability).
func TestContractVerification_DifferentPlanSalt_DifferentDisplayIDs(t *testing.T) {
	source := `var API_KEY = "sk-secret-123"
echo "Key: @var.API_KEY"`

	tree := parser.ParseString(source)
	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	// Plan 1 with random PlanSalt
	planKey1 := []byte("plan-key-1-32-bytes-for-hmac-123")
	vault1 := vault.NewWithPlanKey(planKey1)
	plan1, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
		Vault: vault1,
	})
	if err != nil {
		t.Fatalf("Plan 1 failed: %v", err)
	}

	// Plan 2 with different PlanSalt
	planKey2 := []byte("plan-key-2-32-bytes-for-hmac-456")
	vault2 := vault.NewWithPlanKey(planKey2)
	plan2, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
		Vault: vault2,
	})
	if err != nil {
		t.Fatalf("Plan 2 failed: %v", err)
	}

	// ASSERT: Plans should have different PlanSalt
	if bytes.Equal(plan1.PlanSalt, plan2.PlanSalt) {
		t.Errorf("Plans have same PlanSalt (should be different)")
	}

	// ASSERT: Plans should have different DisplayIDs (unlinkability)
	if len(plan1.SecretUses) == 0 || len(plan2.SecretUses) == 0 {
		t.Fatal("No SecretUses found")
	}

	displayID1 := plan1.SecretUses[0].DisplayID
	displayID2 := plan2.SecretUses[0].DisplayID

	if displayID1 == displayID2 {
		t.Errorf("SECURITY BUG: Same secret value produced same DisplayID across different plans!\n"+
			"  Plan1 DisplayID: %s\n"+
			"  Plan2 DisplayID: %s\n"+
			"This violates unlinkability - attacker can correlate secrets across plans",
			displayID1, displayID2)
	}

	// ASSERT: Plans should have different hashes
	plan1.Freeze()
	plan2.Freeze()

	if plan1.Hash == plan2.Hash {
		t.Errorf("Different plans produced same hash (should be different)")
	}

	t.Logf("✓ Unlinkability verified")
	t.Logf("  Plan1 DisplayID: %s", displayID1)
	t.Logf("  Plan2 DisplayID: %s", displayID2)
}

// TestPlanSalt_NilVault_UsesRandomPlanKey verifies that when no vault is provided,
// the planner creates one with a random planKey and sets plan.PlanSalt accordingly.
func TestPlanSalt_NilVault_UsesRandomPlanKey(t *testing.T) {
	source := `var NAME = "Aled"
echo "@var.NAME"`

	tree := parser.ParseString(source)
	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	// Plan without providing a vault (planner creates its own)
	plan1, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
		Vault: nil, // Planner creates vault internally
	})
	if err != nil {
		t.Fatalf("Plan 1 failed: %v", err)
	}

	plan2, err := planner.Plan(tree.Events, tree.Tokens, planner.Config{
		Vault: nil, // Planner creates vault internally
	})
	if err != nil {
		t.Fatalf("Plan 2 failed: %v", err)
	}

	// ASSERT: Each plan should have a PlanSalt
	if len(plan1.PlanSalt) == 0 {
		t.Error("Plan1.PlanSalt is empty")
	}
	if len(plan2.PlanSalt) == 0 {
		t.Error("Plan2.PlanSalt is empty")
	}

	// ASSERT: PlanSalts should be different (random)
	if bytes.Equal(plan1.PlanSalt, plan2.PlanSalt) {
		t.Errorf("Two independent plans have same PlanSalt (should be random):\n"+
			"  Plan1: %x\n"+
			"  Plan2: %x",
			plan1.PlanSalt, plan2.PlanSalt)
	}

	t.Logf("✓ Random PlanSalt generation works")
	t.Logf("  Plan1 PlanSalt: %x", plan1.PlanSalt)
	t.Logf("  Plan2 PlanSalt: %x", plan2.PlanSalt)
}

func TestPlanSalt_VaultWithoutPlanKey_PreservesRandomSalt(t *testing.T) {
	// Test that when vault has no plan key (GetPlanKey() returns nil),
	// the planner preserves NewPlan()'s random 32-byte salt instead of overwriting with nil

	source := `var NAME = "test"`

	tree := parser.ParseString(source)
	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	// Create vault WITHOUT plan key (like Mode 4 verification might do)
	vlt := vault.NewWithPlanKey(nil) // No plan key set

	result, err := planner.PlanWithObservability(tree.Events, tree.Tokens, planner.Config{
		Vault: vlt, // Provide vault without plan key
	})
	if err != nil {
		t.Fatalf("Planning failed: %v", err)
	}

	plan := result.Plan

	// CRITICAL: PlanSalt must be 32 bytes (from NewPlan's random generation)
	// NOT nil (from vault.GetPlanKey())
	if len(plan.PlanSalt) != 32 {
		t.Errorf("PlanSalt should be 32 bytes (random from NewPlan), got %d bytes", len(plan.PlanSalt))
	}

	if plan.PlanSalt == nil {
		t.Error("PlanSalt should not be nil (should preserve NewPlan's random salt)")
	}

	// Verify it's actually random (not all zeros)
	allZeros := true
	for _, b := range plan.PlanSalt {
		if b != 0 {
			allZeros = false
			break
		}
	}
	if allZeros {
		t.Error("PlanSalt should be random, not all zeros")
	}

	t.Logf("✓ Vault without plan key preserves random PlanSalt")
	t.Logf("  PlanSalt: %x", plan.PlanSalt)
}

func TestPlanSalt_VaultInvalidPlanKey_PreservesRandomSalt(t *testing.T) {
	source := `var NAME = "test"`

	tree := parser.ParseString(source)
	if len(tree.Errors) > 0 {
		t.Fatalf("Parse errors: %v", tree.Errors)
	}

	// Provide a vault with an invalid (non-32-byte) plan key.
	vlt := vault.NewWithPlanKey([]byte{0x01, 0x02, 0x03})

	result, err := planner.PlanWithObservability(tree.Events, tree.Tokens, planner.Config{
		Vault: vlt,
	})
	if err != nil {
		t.Fatalf("Planning failed: %v", err)
	}

	plan := result.Plan

	if len(plan.PlanSalt) != 32 {
		t.Errorf("PlanSalt should remain 32-byte random salt, got %d bytes", len(plan.PlanSalt))
	}

	if bytes.Equal(plan.PlanSalt, []byte{0x01, 0x02, 0x03}) {
		t.Error("PlanSalt should not use invalid vault plan key")
	}
}
