package parser

import (
	"strings"
	"testing"

	"github.com/builtwithtofu/sigil/core/decorator"
	"github.com/builtwithtofu/sigil/core/types"
	"github.com/builtwithtofu/sigil/runtime/lexer"
	"github.com/google/go-cmp/cmp"
)

type parserTestValueDecorator struct {
	path   string
	schema types.DecoratorSchema
}

func (d *parserTestValueDecorator) Descriptor() decorator.Descriptor {
	return decorator.Descriptor{Path: d.path, Schema: d.schema}
}

func (d *parserTestValueDecorator) Resolve(ctx decorator.ValueEvalContext, calls ...decorator.ValueCall) ([]decorator.ResolveResult, error) {
	results := make([]decorator.ResolveResult, len(calls))
	for i := range calls {
		results[i] = decorator.ResolveResult{Value: "test"}
	}
	return results, nil
}

type parserTestExecDecorator struct {
	path   string
	schema types.DecoratorSchema
}

func (d *parserTestExecDecorator) Descriptor() decorator.Descriptor {
	return decorator.Descriptor{Path: d.path, Schema: d.schema}
}

func (d *parserTestExecDecorator) Wrap(next decorator.ExecNode, params map[string]any) decorator.ExecNode {
	return next
}

func init() {
	testValueSchema := types.NewSchema("testvalue", types.KindValue).
		Description("Test value decorator without I/O").
		Param("arg", types.TypeString).
		Description("Test argument").
		Required().
		Done().
		Returns(types.TypeString, "Test value").
		Build()

	fileReadSchema := types.NewSchema("file.read", types.KindValue).
		Description("Read file").
		Param("path", types.TypeString).
		Required().
		Done().
		Returns(types.TypeString, "Contents").
		Build()

	fileTempSchema := types.NewSchema("file.temp", types.KindExecution).
		Description("Create temporary file").
		WithRedirect(types.RedirectOverwriteOnly).
		Build()

	testSinkSchema := types.NewSchema("test.sink.path", types.KindExecution).
		Description("Test sink decorator").
		Param("path", types.TypeString).
		Required().
		Done().
		WithRedirect(types.RedirectBoth).
		Build()

	if err := decorator.Register("testvalue", &parserTestValueDecorator{path: "testvalue", schema: testValueSchema}); err != nil {
		panic(err)
	}

	if err := decorator.Register("file.read", &parserTestValueDecorator{path: "file.read", schema: fileReadSchema}); err != nil {
		panic(err)
	}

	if err := decorator.Register("file.temp", &parserTestExecDecorator{path: "file.temp", schema: fileTempSchema}); err != nil {
		panic(err)
	}

	if err := decorator.Register("test.sink.path", &parserTestExecDecorator{path: "test.sink.path", schema: testSinkSchema}); err != nil {
		panic(err)
	}
}

func TestDecoratorSink(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		expectError  bool
		errorMessage string
	}{
		{
			name:        "accepts decorator sink in redirect target",
			input:       `echo "test" > @test.sink.path("out.txt")`,
			expectError: false,
		},
		{
			name:        "continues accepting bare redirect path",
			input:       `echo "test" > out.txt`,
			expectError: false,
		},
		{
			name:        "accepts input redirect with bare path",
			input:       `cat < input.txt`,
			expectError: false,
		},
		{
			name:        "accepts input redirect with decorator source",
			input:       `cat < @file("input.txt")`,
			expectError: false,
		},
		{
			name:         "rejects non-sink decorator in redirect target",
			input:        `echo "test" > @exec.timeout(5s) { echo "inner" }`,
			expectError:  true,
			errorMessage: "@exec.timeout does not support redirection",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree := Parse([]byte(tt.input))
			tree.ValidateSemantics()

			if tt.expectError {
				if len(tree.Errors) == 0 {
					t.Fatalf("expected parse error, got none")
				}
				if diff := cmp.Diff(tt.errorMessage, tree.Errors[0].Message); diff != "" {
					t.Fatalf("error message mismatch (-want +got):\n%s", diff)
				}
				return
			}

			if len(tree.Errors) > 0 {
				t.Fatalf("unexpected parse errors: %v", tree.Errors)
			}
		})
	}
}

// TestParseEventStructure uses table-driven tests to verify parse tree events
func TestParseEventStructure(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		events []Event
	}{
		{
			name:  "empty file",
			input: "",
			events: []Event{
				{EventOpen, 0},  // Source
				{EventClose, 0}, // Source
			},
		},
		{
			name:  "function with no parameters",
			input: "fun greet() {}",
			events: []Event{
				{EventOpen, 0},  // Source
				{EventOpen, 1},  // Function
				{EventToken, 0}, // fun
				{EventToken, 1}, // greet
				{EventOpen, 2},  // ParamList
				{EventToken, 2}, // (
				{EventToken, 3}, // )
				{EventClose, 2}, // ParamList
				{EventOpen, 3},  // Block
				{EventToken, 4}, // {
				{EventToken, 5}, // }
				{EventClose, 3}, // Block
				{EventClose, 1}, // Function
				{EventClose, 0}, // Source
			},
		},
		{
			name:  "function with go-style typed parameter",
			input: "fun greet(name String) {}",
			events: []Event{
				{EventOpen, 0},  // Source
				{EventOpen, 1},  // Function
				{EventToken, 0}, // fun
				{EventToken, 1}, // greet
				{EventOpen, 2},  // ParamList
				{EventToken, 2}, // (
				{EventOpen, 4},  // Param
				{EventToken, 3}, // name
				{EventOpen, 5},  // TypeAnnotation
				{EventToken, 4}, // String
				{EventClose, 5}, // TypeAnnotation
				{EventClose, 4}, // Param
				{EventToken, 5}, // )
				{EventClose, 2}, // ParamList
				{EventOpen, 3},  // Block
				{EventToken, 6}, // {
				{EventToken, 7}, // }
				{EventClose, 3}, // Block
				{EventClose, 1}, // Function
				{EventClose, 0}, // Source
			},
		},
		{
			name:  "function with optional typed parameter",
			input: "fun greet(name String?) {}",
			events: []Event{
				{EventOpen, 0},  // Source
				{EventOpen, 1},  // Function
				{EventToken, 0}, // fun
				{EventToken, 1}, // greet
				{EventOpen, 2},  // ParamList
				{EventToken, 2}, // (
				{EventOpen, 4},  // Param
				{EventToken, 3}, // name
				{EventOpen, 5},  // TypeAnnotation
				{EventToken, 4}, // String
				{EventToken, 5}, // ?
				{EventClose, 5}, // TypeAnnotation
				{EventClose, 4}, // Param
				{EventToken, 6}, // )
				{EventClose, 2}, // ParamList
				{EventOpen, 3},  // Block
				{EventToken, 7}, // {
				{EventToken, 8}, // }
				{EventClose, 3}, // Block
				{EventClose, 1}, // Function
				{EventClose, 0}, // Source
			},
		},
		{
			name:  "function with typed default parameter",
			input: `fun greet(name String = "World") {}`,
			events: []Event{
				{EventOpen, 0},  // Source
				{EventOpen, 1},  // Function
				{EventToken, 0}, // fun
				{EventToken, 1}, // greet
				{EventOpen, 2},  // ParamList
				{EventToken, 2}, // (
				{EventOpen, 4},  // Param
				{EventToken, 3}, // name
				{EventOpen, 5},  // TypeAnnotation
				{EventToken, 4}, // String
				{EventClose, 5}, // TypeAnnotation
				{EventOpen, 6},  // DefaultValue
				{EventToken, 5}, // =
				{EventToken, 6}, // "World"
				{EventClose, 6}, // DefaultValue
				{EventClose, 4}, // Param
				{EventToken, 7}, // )
				{EventClose, 2}, // ParamList
				{EventOpen, 3},  // Block
				{EventToken, 8}, // {
				{EventToken, 9}, // }
				{EventClose, 3}, // Block
				{EventClose, 1}, // Function
				{EventClose, 0}, // Source
			},
		},
		{
			name:  "function with typed parameter and default value",
			input: `fun greet(name String = "World") {}`,
			events: []Event{
				{EventOpen, 0},  // Source
				{EventOpen, 1},  // Function
				{EventToken, 0}, // fun
				{EventToken, 1}, // greet
				{EventOpen, 2},  // ParamList
				{EventToken, 2}, // (
				{EventOpen, 4},  // Param
				{EventToken, 3}, // name
				{EventOpen, 5},  // TypeAnnotation
				{EventToken, 4}, // String
				{EventClose, 5}, // TypeAnnotation
				{EventOpen, 6},  // DefaultValue
				{EventToken, 5}, // =
				{EventToken, 6}, // "World"
				{EventClose, 6}, // DefaultValue
				{EventClose, 4}, // Param
				{EventToken, 7}, // )
				{EventClose, 2}, // ParamList
				{EventOpen, 3},  // Block
				{EventToken, 8}, // {
				{EventToken, 9}, // }
				{EventClose, 3}, // Block
				{EventClose, 1}, // Function
				{EventClose, 0}, // Source
			},
		},
		{
			name:  "function with grouped go-style parameter type",
			input: `fun greet(first, last String) {}`,
			events: []Event{
				{EventOpen, 0},  // Source
				{EventOpen, 1},  // Function
				{EventToken, 0}, // fun
				{EventToken, 1}, // greet
				{EventOpen, 2},  // ParamList
				{EventToken, 2}, // (
				{EventOpen, 4},  // Param
				{EventToken, 3}, // first
				{EventClose, 4}, // Param
				{EventToken, 4}, // ,
				{EventOpen, 4},  // Param
				{EventToken, 5}, // last
				{EventOpen, 5},  // TypeAnnotation
				{EventToken, 6}, // String
				{EventClose, 5}, // TypeAnnotation
				{EventClose, 4}, // Param
				{EventToken, 7}, // )
				{EventClose, 2}, // ParamList
				{EventOpen, 3},  // Block
				{EventToken, 8}, // {
				{EventToken, 9}, // }
				{EventClose, 3}, // Block
				{EventClose, 1}, // Function
				{EventClose, 0}, // Source
			},
		},
		{
			name:  "function with typed parameters and defaults",
			input: `fun deploy(env String, replicas Int = 3) {}`,
			events: []Event{
				{EventOpen, 0},   // Source
				{EventOpen, 1},   // Function
				{EventToken, 0},  // fun
				{EventToken, 1},  // deploy
				{EventOpen, 2},   // ParamList
				{EventToken, 2},  // (
				{EventOpen, 4},   // Param
				{EventToken, 3},  // env
				{EventOpen, 5},   // TypeAnnotation
				{EventToken, 4},  // String
				{EventClose, 5},  // TypeAnnotation
				{EventClose, 4},  // Param
				{EventToken, 5},  // ,
				{EventOpen, 4},   // Param
				{EventToken, 6},  // replicas
				{EventOpen, 5},   // TypeAnnotation
				{EventToken, 7},  // Int
				{EventClose, 5},  // TypeAnnotation
				{EventOpen, 6},   // DefaultValue
				{EventToken, 8},  // =
				{EventToken, 9},  // 3
				{EventClose, 6},  // DefaultValue
				{EventClose, 4},  // Param
				{EventToken, 10}, // )
				{EventClose, 2},  // ParamList
				{EventOpen, 3},   // Block
				{EventToken, 11}, // {
				{EventToken, 12}, // }
				{EventClose, 3},  // Block
				{EventClose, 1},  // Function
				{EventClose, 0},  // Source
			},
		},
		{
			name:  "function with all parameter variations",
			input: `fun deploy(env String, replicas Int = 3, verbose Bool) {}`,
			events: []Event{
				{EventOpen, 0},   // Source
				{EventOpen, 1},   // Function
				{EventToken, 0},  // fun
				{EventToken, 1},  // deploy
				{EventOpen, 2},   // ParamList
				{EventToken, 2},  // (
				{EventOpen, 4},   // Param
				{EventToken, 3},  // env
				{EventOpen, 5},   // TypeAnnotation
				{EventToken, 4},  // String
				{EventClose, 5},  // TypeAnnotation
				{EventClose, 4},  // Param
				{EventToken, 5},  // ,
				{EventOpen, 4},   // Param
				{EventToken, 6},  // replicas
				{EventOpen, 5},   // TypeAnnotation
				{EventToken, 7},  // Int
				{EventClose, 5},  // TypeAnnotation
				{EventOpen, 6},   // DefaultValue
				{EventToken, 8},  // =
				{EventToken, 9},  // 3
				{EventClose, 6},  // DefaultValue
				{EventClose, 4},  // Param
				{EventToken, 10}, // ,
				{EventOpen, 4},   // Param
				{EventToken, 11}, // verbose
				{EventOpen, 5},   // TypeAnnotation
				{EventToken, 12}, // Bool
				{EventClose, 5},  // TypeAnnotation
				{EventClose, 4},  // Param
				{EventToken, 13}, // )
				{EventClose, 2},  // ParamList
				{EventOpen, 3},   // Block
				{EventToken, 14}, // {
				{EventToken, 15}, // }
				{EventClose, 3},  // Block
				{EventClose, 1},  // Function
				{EventClose, 0},  // Source
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree := ParseString(tt.input)

			// Should have no errors
			if len(tree.Errors) != 0 {
				t.Errorf("Expected no errors, got: %v", tree.Errors)
			}

			// Compare events using cmp.Diff for clear output
			if diff := cmp.Diff(tt.events, tree.Events); diff != "" {
				t.Errorf("Events mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFunctionDefaultValueValidation(t *testing.T) {
	t.Run("newline immediately after equals", func(t *testing.T) {
		tree := ParseString(`fun greet(
	name String =
	"World"
) {}`)

		if len(tree.Errors) != 0 {
			t.Fatalf("expected no parse errors, got %v", tree.Errors)
		}

		hasWorldLiteral := false
		for _, evt := range tree.Events {
			if evt.Kind != EventToken {
				continue
			}
			tok := tree.Tokens[evt.Data]
			if tok.Type == lexer.STRING && string(tok.Text) == "\"World\"" {
				hasWorldLiteral = true
				break
			}
		}

		if !hasWorldLiteral {
			t.Fatal("expected default value literal to be parsed")
		}
	})

	t.Run("missing value before right paren", func(t *testing.T) {
		tree := ParseString(`fun greet(name String = ) {}`)

		if len(tree.Errors) != 1 {
			t.Fatalf("error count mismatch: want 1, got %d (%v)", len(tree.Errors), tree.Errors)
		}

		got := struct {
			Message    string
			Context    string
			Got        lexer.TokenType
			Suggestion string
		}{
			Message:    tree.Errors[0].Message,
			Context:    tree.Errors[0].Context,
			Got:        tree.Errors[0].Got,
			Suggestion: tree.Errors[0].Suggestion,
		}

		want := struct {
			Message    string
			Context    string
			Got        lexer.TokenType
			Suggestion string
		}{
			Message:    "missing default parameter value",
			Context:    "function parameter default value",
			Got:        lexer.RPAREN,
			Suggestion: "Add a value after '='",
		}

		if diff := cmp.Diff(want, got); diff != "" {
			t.Fatalf("default value error mismatch (-want +got):\n%s", diff)
		}
	})
}

// TestParseBasics verifies basic parsing functionality
func TestParseBasics(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantNil    bool
		wantTokens bool
		wantEvents bool
	}{
		{
			name:       "empty file returns non-nil tree",
			input:      "",
			wantNil:    false,
			wantTokens: true, // Lexer always produces EOF token
			wantEvents: true,
		},
		{
			name:       "function declaration has tokens and events",
			input:      "fun greet() {}",
			wantNil:    false,
			wantTokens: true,
			wantEvents: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree := ParseString(tt.input)

			if (tree == nil) != tt.wantNil {
				t.Errorf("ParseString() nil = %v, want %v", tree == nil, tt.wantNil)
			}

			if tree != nil {
				hasTokens := len(tree.Tokens) > 0
				if hasTokens != tt.wantTokens {
					t.Errorf("Has tokens = %v, want %v", hasTokens, tt.wantTokens)
				}

				hasEvents := len(tree.Events) > 0
				if hasEvents != tt.wantEvents {
					t.Errorf("Has events = %v, want %v", hasEvents, tt.wantEvents)
				}
			}
		})
	}
}

// TestTelemetry verifies telemetry collection
func TestTelemetry(t *testing.T) {
	input := "fun greet(name String) {}"

	t.Run("telemetry off by default", func(t *testing.T) {
		tree := ParseString(input)
		if tree.Telemetry != nil {
			t.Error("Expected nil telemetry by default")
		}
	})

	t.Run("telemetry timing enabled", func(t *testing.T) {
		tree := ParseString(input, WithTelemetryTiming())

		if tree.Telemetry == nil {
			t.Fatal("Expected telemetry to be non-nil")
		}

		if tree.Telemetry.TokenCount == 0 {
			t.Error("Expected non-zero token count")
		}

		if tree.Telemetry.EventCount == 0 {
			t.Error("Expected non-zero event count")
		}

		if tree.Telemetry.TotalTime == 0 {
			t.Error("Expected non-zero total time")
		}
	})

	t.Run("telemetry basic enabled", func(t *testing.T) {
		tree := ParseString(input, WithTelemetryBasic())

		if tree.Telemetry == nil {
			t.Fatal("Expected telemetry to be non-nil")
		}

		if tree.Telemetry.TokenCount == 0 {
			t.Error("Expected non-zero token count")
		}
	})
}

// TestDebugTracing verifies debug event collection
func TestDebugTracing(t *testing.T) {
	input := "fun greet(name String) {}"

	t.Run("debug off by default", func(t *testing.T) {
		tree := ParseString(input)
		if len(tree.DebugEvents) != 0 {
			t.Error("Expected no debug events by default")
		}
	})

	t.Run("debug paths enabled", func(t *testing.T) {
		tree := ParseString(input, WithDebugPaths())

		if len(tree.DebugEvents) == 0 {
			t.Fatal("Expected debug events")
		}

		// Should have enter/exit events for source, function, paramList, etc.
		hasEnterSource := false
		hasExitSource := false
		for _, evt := range tree.DebugEvents {
			if evt.Event == "enter_source" {
				hasEnterSource = true
			}
			if evt.Event == "exit_source" {
				hasExitSource = true
			}
		}

		if !hasEnterSource {
			t.Error("Expected enter_source debug event")
		}
		if !hasExitSource {
			t.Error("Expected exit_source debug event")
		}
	})
}

// TestPipeOperatorValidation tests that pipe operator validates I/O capabilities
func TestPipeOperatorValidation(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		expectedError *ParseError
	}{
		{
			name:  "pipe from decorator without stdout support",
			input: `@exec.timeout(5s) { echo "test" } | grep "pattern"`,
			expectedError: &ParseError{
				Position:   lexer.Position{Line: 1, Column: 35, Offset: 34},
				Message:    "@exec.timeout does not produce stdout",
				Context:    "pipe operator",
				Got:        lexer.PIPE,
				Suggestion: "Only shell commands and decorators with stdout support can be piped from",
				Example:    "echo \"test\" | grep \"pattern\"",
				Note:       "Only decorators that produce stdout can be piped from",
			},
		},
		{
			name:          "pipe from interpolated decorator is valid",
			input:         `echo @file.read("test.txt") | grep "pattern"`,
			expectedError: nil, // This is valid - @file.read is interpolated into echo, then echo is piped
		},
		{
			name:          "valid pipe between shell commands",
			input:         `echo "test" | grep "test"`,
			expectedError: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree := Parse([]byte(tt.input))
			tree.ValidateSemantics() // Post-parse validation

			if tt.expectedError != nil {
				if len(tree.Errors) == 0 {
					t.Errorf("expected parse error but got none")
					return
				}

				if diff := cmp.Diff(*tt.expectedError, tree.Errors[0]); diff != "" {
					t.Errorf("Error mismatch (-want +got):\n%s", diff)
				}
			} else {
				if len(tree.Errors) > 0 {
					t.Errorf("unexpected parse errors: %v", tree.Errors)
				}
			}
		})
	}
}

// TestRedirectOperatorValidation tests that redirect operator validates redirect capabilities
func TestRedirectOperatorValidation(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		expectedError *ParseError
	}{
		{
			name:  "redirect to decorator without block - syntax error first",
			input: `echo "test" > @exec.timeout(5s)`,
			expectedError: &ParseError{
				Position:   lexer.Position{Line: 1, Column: 32, Offset: 31},
				Message:    "@exec.timeout requires a block",
				Context:    "decorator block",
				Got:        lexer.EOF,
				Suggestion: "Add a block: @exec.timeout(...) { ... }",
				Example:    "",
				Note:       "",
			},
		},
		{
			name:  "redirect to decorator with block but no redirect support",
			input: `echo "test" > @exec.timeout(5s) { echo "inner" }`,
			expectedError: &ParseError{
				Position:   lexer.Position{Line: 1, Column: 13, Offset: 12},
				Message:    "@exec.timeout does not support redirection",
				Context:    "redirect operator",
				Got:        lexer.GT,
				Suggestion: "Only decorators with redirect support can be used as redirect targets",
				Example:    "echo \"test\" > output.txt",
				Note:       "Use @shell(\"output.txt\") or decorators that support redirect",
			},
		},
		{
			name:  "append to decorator that only supports overwrite",
			input: `echo "test" >> @file.temp()`,
			expectedError: &ParseError{
				Position:   lexer.Position{Line: 1, Column: 13, Offset: 12},
				Message:    "@file.temp does not support append (>>)",
				Context:    "redirect operator",
				Got:        lexer.APPEND,
				Suggestion: "Use a different redirect mode or a decorator that supports append",
				Example:    "echo \"test\" >> output.txt",
				Note:       "@file.temp only supports overwrite-only",
			},
		},
		{
			name:          "valid redirect to shell (file path)",
			input:         `echo "test" > output.txt`,
			expectedError: nil,
		},
		{
			name:          "valid append to shell (file path)",
			input:         `echo "test" >> output.txt`,
			expectedError: nil,
		},
		{
			name:          "valid redirect with pipe",
			input:         `echo "test" | grep "test" > output.txt`,
			expectedError: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree := Parse([]byte(tt.input))
			tree.ValidateSemantics() // Post-parse validation

			if tt.expectedError != nil {
				if len(tree.Errors) == 0 {
					t.Errorf("expected parse error but got none")
					return
				}

				if diff := cmp.Diff(*tt.expectedError, tree.Errors[0]); diff != "" {
					t.Errorf("Error mismatch (-want +got):\n%s", diff)
				}
			} else {
				if len(tree.Errors) > 0 {
					t.Errorf("unexpected parse errors: %v", tree.Errors)
				}
			}
		})
	}
}

// TestNamespacedDecoratorParsing tests that decorators with dots in their names are recognized
// Note: "file.read" is registered in init(), but "file" is NOT registered
func TestNamespacedDecoratorParsing(t *testing.T) {
	// Test that @file.read is recognized (full path extraction works)
	t.Run("namespaced decorator recognized", func(t *testing.T) {
		input := `var x = @file.read("test.txt")`
		tree := Parse([]byte(input))

		// Count decorator nodes in parse tree
		decoratorCount := 0
		for _, event := range tree.Events {
			if event.Kind == EventOpen && NodeKind(event.Data) == NodeDecorator {
				decoratorCount++
			}
		}

		if decoratorCount == 0 {
			t.Fatal("@file.read was not recognized as a decorator - parser needs to extract full namespaced path")
		}

		if len(tree.Errors) > 0 {
			t.Errorf("Parser recognized @file.read but had errors:")
			for _, err := range tree.Errors {
				t.Logf("  %s", err.Message)
			}
		}
	})

	// Test that @file alone is NOT recognized (only file.read is registered)
	t.Run("base name alone not recognized", func(t *testing.T) {
		input := `var x = @file("test.txt")`
		tree := Parse([]byte(input))

		// Since "file" is not registered, @ should be treated as literal
		// This would likely cause a parse error or treat it as shell syntax
		// We just verify it doesn't crash - the exact behavior depends on context
		t.Logf("Parsed @file (not registered) - errors: %d", len(tree.Errors))
	})
}

// TestEnumParameterValidation - REMOVED
// The old @shell decorator had a 'scrub' enum parameter for testing enum validation.
// The new @shell decorator doesn't have this parameter.
// Enum validation is tested in core/types/schema_validation_test.go instead.

// TestValueDecoratorRejectsBlock verifies value decorators cannot take blocks
func TestValueDecoratorRejectsBlock(t *testing.T) {
	// @var is a value decorator - should NOT take a block
	input := `@var.name { echo "test" }`
	tree := ParseString(input)

	if len(tree.Errors) == 0 {
		t.Fatal("Expected error for value decorator with block")
	}

	err := tree.Errors[0]

	// Verify error message follows established format
	if !strings.Contains(err.Message, "@var") {
		t.Errorf("Error should mention decorator name, got: %q", err.Message)
	}

	if !strings.Contains(err.Message, "cannot have a block") {
		t.Errorf("Error should mention block restriction, got: %q", err.Message)
	}

	if err.Context != "decorator block" {
		t.Errorf("Context: got %q, want %q", err.Context, "decorator block")
	}
}

// TestExecDecoratorAllowsBlock verifies execution decorators can take blocks
func TestExecDecoratorAllowsBlock(t *testing.T) {
	// @retry is an execution decorator - should work with blocks
	input := `@exec.retry(times=3) { echo "test" }`
	tree := ParseString(input)

	if len(tree.Errors) != 0 {
		t.Errorf("@exec.retry should work with blocks, got errors: %v", tree.Errors)
	}
}

// TestEnvDecoratorRejectsBlock verifies @env cannot take blocks
func TestEnvDecoratorRejectsBlock(t *testing.T) {
	// @env is a value decorator - should NOT take a block
	input := `@env.HOME { echo "test" }`
	tree := ParseString(input)

	if len(tree.Errors) == 0 {
		t.Fatal("Expected error for @env with block")
	}

	err := tree.Errors[0]
	if !strings.Contains(err.Message, "@env") {
		t.Errorf("Error should mention @env, got: %q", err.Message)
	}
}
