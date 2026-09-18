package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/builtwithtofu/sigil/core/decorator"
	"github.com/builtwithtofu/sigil/core/invariant"
	"github.com/builtwithtofu/sigil/core/planfmt"
)

// DisplayIDResolver resolves display IDs in an execution transport context.
type DisplayIDResolver interface {
	ResolveDisplayIDWithTransport(displayID, currentTransportID string) (any, error)
}

// Config configures the executor
type Config struct {
	Debug          DebugLevel     // Debug tracing (development only)
	Telemetry      TelemetryLevel // Telemetry collection (production-safe)
	Stderr         io.Writer
	sessionFactory sessionFactory
}

// DebugLevel controls debug tracing (development only)
type DebugLevel int

const (
	DebugOff      DebugLevel = iota // No debug info (default)
	DebugPaths                      // Step entry/exit tracing
	DebugDetailed                   // Command output, timing details
)

// TelemetryLevel controls telemetry collection (production-safe)
type TelemetryLevel int

const (
	TelemetryOff    TelemetryLevel = iota // Zero overhead (default)
	TelemetryBasic                        // Step counts only
	TelemetryTiming                       // Counts + timing per step
)

// ExecutionResult holds the result of plan execution
type ExecutionResult struct {
	ExitCode    int                 // Final exit code (0 = success)
	Duration    time.Duration       // Total execution time
	StepsRun    int                 // Number of steps executed
	Telemetry   *ExecutionTelemetry // Additional metrics (nil if TelemetryOff)
	DebugEvents []DebugEvent        // Debug events (nil if DebugOff)
	Errors      []error             // Structured sink failures; producer status remains in ExitCode.
}

// ExecutionTelemetry holds additional execution metrics (optional, production-safe)
type ExecutionTelemetry struct {
	StepCount   int          // Total steps in plan
	StepsRun    int          // Steps actually executed
	StepTimings []StepTiming // Per-step timing (if TelemetryTiming)
	FailedStep  *uint64      // Step ID that failed (if any)
}

// StepTiming holds timing information for a single step
type StepTiming struct {
	StepID   uint64
	Duration time.Duration
	ExitCode int
}

// DebugEvent represents a debug trace event
type DebugEvent struct {
	Timestamp time.Time
	Event     string // "enter_execute", "step_start", "step_complete", etc.
	StepID    uint64 // Current step ID (0 if not step-specific)
	Context   string // Additional context
}

// executor holds execution state
type executor struct {
	config   Config
	vault    DisplayIDResolver // For DisplayID resolution (nil if no secrets)
	sessions *sessionRuntime
	workers  *shellWorkerPool

	// Execution state
	stepsRun   int
	exitCode   int
	stderr     io.Writer
	sinkMu     sync.Mutex
	sinkErrors []error
	stop       context.CancelCauseFunc

	// Observability
	debugEvents []DebugEvent
	telemetry   *ExecutionTelemetry
	startTime   time.Time
}

// ExecutePlan runs a planfmt plan and returns the result.
//
// This is the plan-native execution boundary for planner/planfmt contracts.
// No planfmt->sdk step conversion is required on this execution path.
func ExecutePlan(ctx context.Context, plan *planfmt.Plan, config Config, vlt DisplayIDResolver) (*ExecutionResult, error) {
	invariant.NotNil(ctx, "ctx")
	invariant.NotNil(plan, "plan")
	if _, err := planfmt.ReviewOutputs(plan); err != nil {
		return nil, fmt.Errorf("invalid plan outputs: %w", err)
	}
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)

	e := &executor{
		config:    config,
		vault:     vlt,
		sessions:  newSessionRuntime(config.sessionFactory),
		workers:   nil,
		stderr:    config.Stderr,
		startTime: time.Now(),
		stop:      stop,
	}
	if e.stderr == nil {
		e.stderr = os.Stderr
	}
	e.workers = newShellWorkerPool(e.sessions)
	e.sessions.registerPlanTransports(plan.Transports)
	defer e.sessions.Close()
	defer e.workers.Close()

	if config.Telemetry != TelemetryOff {
		e.telemetry = &ExecutionTelemetry{
			StepCount: len(plan.Steps),
		}
		if config.Telemetry == TelemetryTiming {
			e.telemetry.StepTimings = make([]StepTiming, 0, len(plan.Steps))
		}
	}

	if config.Debug >= DebugPaths {
		e.recordDebugEvent("enter_execute", 0, fmt.Sprintf("steps=%d", len(plan.Steps)))
	}

	rootExecCtx := newExecutionContext(make(map[string]interface{}), e, ctx)

	for _, step := range plan.Steps {
		stepStart := time.Now()

		if config.Debug >= DebugDetailed {
			e.recordDebugEvent("step_start", step.ID, "executing tree")
		}

		exitCode := e.executePlanStep(rootExecCtx, step)
		e.stepsRun++

		stepDuration := time.Since(stepStart)
		if config.Telemetry == TelemetryTiming {
			e.telemetry.StepTimings = append(e.telemetry.StepTimings, StepTiming{
				StepID:   step.ID,
				Duration: stepDuration,
				ExitCode: exitCode,
			})
		}

		if config.Debug >= DebugDetailed {
			e.recordDebugEvent("step_complete", step.ID, fmt.Sprintf("exit=%d, duration=%v", exitCode, stepDuration))
		}

		if exitCode != 0 {
			e.exitCode = exitCode
			if errors.Is(context.Cause(ctx), decorator.ErrPublicationUnknown) {
				e.exitCode = decorator.ExitFailure
			}
			if e.telemetry != nil {
				stepID := step.ID
				e.telemetry.FailedStep = &stepID
			}
			break
		}
	}

	if e.telemetry != nil {
		e.telemetry.StepsRun = e.stepsRun
	}

	duration := time.Since(e.startTime)
	if config.Debug >= DebugPaths {
		e.recordDebugEvent("exit_execute", 0, fmt.Sprintf("steps_run=%d, exit=%d, duration=%v", e.stepsRun, e.exitCode, duration))
	}

	invariant.Postcondition(
		e.exitCode == decorator.ExitCanceled || (e.exitCode >= 0 && e.exitCode <= 255),
		"exit code must be -1 (canceled) or in range [0, 255], got %d", e.exitCode)
	invariant.Postcondition(e.stepsRun >= 0, "steps run must be non-negative")
	invariant.Postcondition(e.stepsRun <= len(plan.Steps), "steps run cannot exceed total steps")

	if errors.Is(context.Cause(ctx), decorator.ErrPublicationUnknown) {
		e.exitCode = decorator.ExitFailure
	}
	return &ExecutionResult{
		ExitCode:    e.exitCode,
		Duration:    duration,
		StepsRun:    e.stepsRun,
		Telemetry:   e.telemetry,
		DebugEvents: e.debugEvents,
		Errors:      e.sinkErrors,
	}, nil
}

// recordDebugEvent records a debug event (only if debug enabled)
func (e *executor) recordDebugEvent(event string, stepID uint64, contextInfo string) {
	if e.config.Debug == DebugOff {
		return
	}

	e.debugEvents = append(e.debugEvents, DebugEvent{
		Timestamp: time.Now(),
		Event:     event,
		StepID:    stepID,
		Context:   contextInfo,
	})
}
