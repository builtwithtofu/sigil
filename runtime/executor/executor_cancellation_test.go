package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/builtwithtofu/sigil/core/planfmt"
	"github.com/builtwithtofu/sigil/core/sdk"
	_ "github.com/builtwithtofu/sigil/runtime/decorators"
	"github.com/stretchr/testify/assert"
)

const (
	// Cancellation tests intentionally allow headroom for CI host jitter.
	// These bounds still verify runs stop well before the underlying sleep commands complete.
	cancelFastBound      = 6 * time.Second
	cancelVeryFastBound  = 2 * time.Second
	alreadyCanceledBound = 500 * time.Millisecond
)

// TestContextCancellationStopsExecution verifies that cancelling the context
// stops execution immediately without waiting for commands to complete.
func TestContextCancellationStopsExecution(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())

	// Create a long-running command (sleep 10 seconds)
	steps := []sdk.Step{{
		ID: 1,
		Tree: &sdk.CommandNode{
			Name: "@shell",
			Args: map[string]interface{}{
				"command": "sleep 10",
			},
		},
	}}

	// Cancel after 100ms
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	result, err := ExecutePlan(ctx, planFromSDKStepsForCancellation(t, "cancel-context", steps), Config{}, testVault())
	duration := time.Since(start)

	// Should stop well before full 10s sleep duration.
	assert.Less(t, duration, cancelFastBound, "execution should stop quickly after cancellation")

	// Should return non-zero exit code (command was interrupted)
	assert.NotEqual(t, 0, result.ExitCode, "cancelled execution should return non-zero exit code")

	// Error should be nil (cancellation is not an error from Execute's perspective)
	assert.NoError(t, err, "Execute should not return error on cancellation")
}

// TestTimeoutPropagatesThroughRedirects verifies that context timeout
// is respected even when executing redirects.
func TestTimeoutPropagatesThroughRedirects(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	outPath := t.TempDir() + "/test-redirect-timeout.txt"

	// Redirect with infinite output
	steps := []sdk.Step{{
		ID: 1,
		Tree: &sdk.RedirectNode{
			Source: &sdk.CommandNode{
				Name: "@shell",
				Args: map[string]interface{}{
					"command": "yes", // Infinite output
				},
			},
			Sink: &sdk.FsPathSink{Path: outPath},
			Mode: sdk.RedirectOverwrite,
		},
	}}

	start := time.Now()
	result, err := ExecutePlan(ctx, planFromSDKStepsForCancellation(t, "cancel-timeout-redirect", steps), Config{}, testVault())
	duration := time.Since(start)

	// Should timeout quickly relative to command runtime, with CI jitter headroom.
	assert.Less(t, duration, cancelVeryFastBound, "should timeout well before long-running command completes")
	assert.NotEqual(t, 0, result.ExitCode, "timed out execution should return non-zero")
	assert.NoError(t, err)
}

// TestPipelineCancellationStopsAllCommands verifies that cancelling a pipeline
// stops all commands in the pipeline, not just the first one.
func TestPipelineCancellationStopsAllCommands(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())

	// Pipeline with multiple long-running commands
	steps := []sdk.Step{{
		ID: 1,
		Tree: &sdk.PipelineNode{
			Commands: []sdk.TreeNode{
				&sdk.CommandNode{
					Name: "@shell",
					Args: map[string]interface{}{
						"command": "sleep 10",
					},
				},
				&sdk.CommandNode{
					Name: "@shell",
					Args: map[string]interface{}{
						"command": "sleep 10",
					},
				},
				&sdk.CommandNode{
					Name: "@shell",
					Args: map[string]interface{}{
						"command": "sleep 10",
					},
				},
			},
		},
	}}

	// Cancel after 100ms
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, _ = ExecutePlan(ctx, planFromSDKStepsForCancellation(t, "cancel-pipeline", steps), Config{}, testVault())
	duration := time.Since(start)

	// All commands should stop well before the 10s command duration.
	assert.Less(t, duration, cancelFastBound, "all pipeline commands should stop after cancellation")
}

// TestNestedDecoratorCancellation verifies that cancellation propagates
// through nested decorators (e.g., @exec.retry { @exec.timeout { @shell } }).
func TestNestedDecoratorCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Nested decorators: @exec.retry { @shell { sleep 10 } }
	steps := []sdk.Step{{
		ID: 1,
		Tree: &sdk.CommandNode{
			Name: "@exec.retry",
			Args: map[string]interface{}{
				"times": int64(5),
			},
			Block: []sdk.Step{{
				ID: 2,
				Tree: &sdk.CommandNode{
					Name: "@shell",
					Args: map[string]interface{}{
						"command": "sleep 10",
					},
				},
			}},
		},
	}}

	start := time.Now()
	result, err := ExecutePlan(ctx, planFromSDKStepsForCancellation(t, "cancel-nested", steps), Config{}, testVault())
	duration := time.Since(start)

	// Should respect outer timeout, not retry 5 times.
	assert.Less(t, duration, cancelFastBound, "should respect outer timeout")
	assert.NotEqual(t, 0, result.ExitCode)
	assert.NoError(t, err)
}

// TestCancellationDuringExecuteBlock verifies that cancellation works
// when decorators call ExecuteBlock (nested execution).
func TestCancellationDuringExecuteBlock(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())

	// Decorator with block that contains long-running command
	steps := []sdk.Step{{
		ID: 1,
		Tree: &sdk.CommandNode{
			Name: "@shell",
			Args: map[string]interface{}{
				"command": "echo 'before'",
			},
			Block: []sdk.Step{{
				ID: 2,
				Tree: &sdk.CommandNode{
					Name: "@shell",
					Args: map[string]interface{}{
						"command": "sleep 10",
					},
				},
			}},
		},
	}}

	// Cancel after 100ms
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, _ = ExecutePlan(ctx, planFromSDKStepsForCancellation(t, "cancel-execute-block", steps), Config{}, testVault())
	duration := time.Since(start)

	// Should stop before full command duration.
	assert.Less(t, duration, cancelFastBound, "ExecuteBlock should respect cancellation")
}

// TestMultipleCancellations verifies that calling cancel multiple times
// is safe and idempotent.
func TestMultipleCancellations(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())

	steps := []sdk.Step{{
		ID: 1,
		Tree: &sdk.CommandNode{
			Name: "@shell",
			Args: map[string]interface{}{
				"command": "sleep 5",
			},
		},
	}}

	// Cancel multiple times
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
		cancel() // Second call should be safe
		cancel() // Third call should be safe
	}()

	start := time.Now()
	result, err := ExecutePlan(ctx, planFromSDKStepsForCancellation(t, "cancel-multi", steps), Config{}, testVault())
	duration := time.Since(start)

	assert.Less(t, duration, cancelFastBound)
	assert.NotEqual(t, 0, result.ExitCode)
	assert.NoError(t, err)
}

// TestCancellationWithSequenceOperator verifies that cancellation works
// with sequence operators (;).
func TestCancellationWithSequenceOperator(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())

	// Sequence: cmd1 ; cmd2 ; cmd3 (all long-running)
	steps := []sdk.Step{{
		ID: 1,
		Tree: &sdk.SequenceNode{
			Nodes: []sdk.TreeNode{
				&sdk.CommandNode{
					Name: "@shell",
					Args: map[string]interface{}{
						"command": "sleep 10",
					},
				},
				&sdk.CommandNode{
					Name: "@shell",
					Args: map[string]interface{}{
						"command": "sleep 10",
					},
				},
				&sdk.CommandNode{
					Name: "@shell",
					Args: map[string]interface{}{
						"command": "sleep 10",
					},
				},
			},
		},
	}}

	// Cancel after 100ms
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, _ = ExecutePlan(ctx, planFromSDKStepsForCancellation(t, "cancel-sequence", steps), Config{}, testVault())
	duration := time.Since(start)

	// Should stop before all 3 commands could run.
	assert.Less(t, duration, cancelFastBound, "sequence should stop after cancellation")
}

func TestCancellationStopsSchedulingSubsequentSequenceNodes(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	markerPath := filepath.Join(t.TempDir(), "should-not-exist.txt")

	steps := []sdk.Step{{
		ID: 1,
		Tree: &sdk.SequenceNode{
			Nodes: []sdk.TreeNode{
				&sdk.CommandNode{
					Name: "@shell",
					Args: map[string]interface{}{
						"command": "sleep 10",
					},
				},
				&sdk.CommandNode{
					Name: "@shell",
					Args: map[string]interface{}{
						"command": "echo ran > " + markerPath,
					},
				},
			},
		},
	}}

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	result, err := ExecutePlan(ctx, planFromSDKStepsForCancellation(t, "cancel-sequence-no-schedule", steps), Config{}, testVault())
	duration := time.Since(start)

	assert.Less(t, duration, cancelFastBound, "execution should stop promptly after cancellation")
	assert.NotEqual(t, 0, result.ExitCode, "cancelled run should be non-zero")
	assert.NoError(t, err)

	_, statErr := os.Stat(markerPath)
	assert.True(t, os.IsNotExist(statErr), "second sequence node should not run after cancellation")
}

// TestCancellationWithAndOperator verifies that cancellation works
// with AND operators (&&).
func TestCancellationWithAndOperator(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())

	// AND: cmd1 && cmd2 (both long-running)
	steps := []sdk.Step{{
		ID: 1,
		Tree: &sdk.AndNode{
			Left: &sdk.CommandNode{
				Name: "@shell",
				Args: map[string]interface{}{
					"command": "sleep 10",
				},
			},
			Right: &sdk.CommandNode{
				Name: "@shell",
				Args: map[string]interface{}{
					"command": "sleep 10",
				},
			},
		},
	}}

	// Cancel after 100ms
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, _ = ExecutePlan(ctx, planFromSDKStepsForCancellation(t, "cancel-and", steps), Config{}, testVault())
	duration := time.Since(start)

	// Should stop before full command duration.
	assert.Less(t, duration, cancelFastBound, "AND operator should respect cancellation")
}

// TestCancellationWithOrOperator verifies that cancellation works
// with OR operators (||).
func TestCancellationWithOrOperator(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())

	// OR: cmd1 || cmd2 (both long-running)
	steps := []sdk.Step{{
		ID: 1,
		Tree: &sdk.OrNode{
			Left: &sdk.CommandNode{
				Name: "@shell",
				Args: map[string]interface{}{
					"command": "sleep 10",
				},
			},
			Right: &sdk.CommandNode{
				Name: "@shell",
				Args: map[string]interface{}{
					"command": "sleep 10",
				},
			},
		},
	}}

	// Cancel after 100ms
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, _ = ExecutePlan(ctx, planFromSDKStepsForCancellation(t, "cancel-or", steps), Config{}, testVault())
	duration := time.Since(start)

	// Should stop before full command duration.
	assert.Less(t, duration, cancelFastBound, "OR operator should respect cancellation")
}

// TestDeadlineExceeded verifies that context deadline is properly detected.
func TestDeadlineExceeded(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(100*time.Millisecond))
	defer cancel()

	steps := []sdk.Step{{
		ID: 1,
		Tree: &sdk.CommandNode{
			Name: "@shell",
			Args: map[string]interface{}{
				"command": "sleep 10",
			},
		},
	}}

	start := time.Now()
	result, err := ExecutePlan(ctx, planFromSDKStepsForCancellation(t, "cancel-deadline", steps), Config{}, testVault())
	duration := time.Since(start)

	// Should stop after deadline, with host jitter headroom.
	assert.Less(t, duration, cancelFastBound, "should stop after deadline")
	assert.NotEqual(t, 0, result.ExitCode)
	assert.NoError(t, err)

	// Context should report deadline exceeded
	assert.Equal(t, context.DeadlineExceeded, ctx.Err())
}

// TestAlreadyCancelledContext verifies that passing an already-cancelled
// context doesn't execute anything.
func TestAlreadyCancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	steps := []sdk.Step{{
		ID: 1,
		Tree: &sdk.CommandNode{
			Name: "@shell",
			Args: map[string]interface{}{
				"command": "echo 'should not run'",
			},
		},
	}}

	start := time.Now()
	result, err := ExecutePlan(ctx, planFromSDKStepsForCancellation(t, "cancel-already", steps), Config{}, testVault())
	duration := time.Since(start)

	// Should return almost immediately for already-cancelled context.
	assert.Less(t, duration, alreadyCanceledBound, "should return immediately with cancelled context")
	assert.NotEqual(t, 0, result.ExitCode, "should return non-zero for cancelled context")
	assert.NoError(t, err)
}

func planFromSDKStepsForCancellation(t *testing.T, target string, steps []sdk.Step) *planfmt.Plan {
	t.Helper()

	planSteps := make([]planfmt.Step, len(steps))
	for i, step := range steps {
		planSteps[i] = planfmt.Step{ID: step.ID, Tree: planNodeFromSDKForCancellation(t, step.Tree)}
	}

	return &planfmt.Plan{Target: target, Steps: planSteps}
}

func planNodeFromSDKForCancellation(t *testing.T, node sdk.TreeNode) planfmt.ExecutionNode {
	t.Helper()

	switch n := node.(type) {
	case *sdk.CommandNode:
		args := make([]planfmt.Arg, 0, len(n.Args))
		for key, value := range n.Args {
			args = append(args, planfmt.Arg{Key: key, Val: planValueFromSDKForCancellation(t, value)})
		}
		block := make([]planfmt.Step, len(n.Block))
		for i, step := range n.Block {
			block[i] = planfmt.Step{ID: step.ID, Tree: planNodeFromSDKForCancellation(t, step.Tree)}
		}
		return &planfmt.CommandNode{Decorator: n.Name, TransportID: n.TransportID, Args: args, Block: block}
	case *sdk.PipelineNode:
		commands := make([]planfmt.ExecutionNode, len(n.Commands))
		for i, child := range n.Commands {
			commands[i] = planNodeFromSDKForCancellation(t, child)
		}
		return &planfmt.PipelineNode{Commands: commands}
	case *sdk.RedirectNode:
		sink, ok := n.Sink.(*sdk.FsPathSink)
		if !ok {
			t.Fatalf("unsupported sdk sink for plan conversion: %T", n.Sink)
		}
		return &planfmt.RedirectNode{
			Source: planNodeFromSDKForCancellation(t, n.Source),
			Target: planfmt.EndpointSpec{
				Decorator: "@file",
				Args:      []planfmt.Arg{{Key: "path", Val: planfmt.Value{Kind: planfmt.ValueString, Str: sink.Path}}},
			},
			Mode: planfmt.RedirectMode(n.Mode),
		}
	case *sdk.AndNode:
		return &planfmt.AndNode{Left: planNodeFromSDKForCancellation(t, n.Left), Right: planNodeFromSDKForCancellation(t, n.Right)}
	case *sdk.OrNode:
		return &planfmt.OrNode{Left: planNodeFromSDKForCancellation(t, n.Left), Right: planNodeFromSDKForCancellation(t, n.Right)}
	case *sdk.SequenceNode:
		nodes := make([]planfmt.ExecutionNode, len(n.Nodes))
		for i, child := range n.Nodes {
			nodes[i] = planNodeFromSDKForCancellation(t, child)
		}
		return &planfmt.SequenceNode{Nodes: nodes}
	default:
		t.Fatalf("unsupported sdk node for plan conversion: %T", node)
		return nil
	}
}

func planValueFromSDKForCancellation(t *testing.T, value any) planfmt.Value {
	t.Helper()

	switch v := value.(type) {
	case string:
		return planfmt.Value{Kind: planfmt.ValueString, Str: v}
	case int:
		return planfmt.Value{Kind: planfmt.ValueInt, Int: int64(v)}
	case int64:
		return planfmt.Value{Kind: planfmt.ValueInt, Int: v}
	case bool:
		return planfmt.Value{Kind: planfmt.ValueBool, Bool: v}
	case float64:
		return planfmt.Value{Kind: planfmt.ValueFloat, Float: v}
	default:
		t.Fatalf("unsupported sdk arg value for plan conversion: %T", value)
		return planfmt.Value{}
	}
}
