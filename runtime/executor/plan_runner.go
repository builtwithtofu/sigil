package executor

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/builtwithtofu/sigil/core/decorator"
	"github.com/builtwithtofu/sigil/core/invariant"
	"github.com/builtwithtofu/sigil/core/planfmt"
	"github.com/builtwithtofu/sigil/core/sdk"
)

func (e *executor) executePlanStep(execCtx sdk.ExecutionContext, step planfmt.Step) int {
	invariant.NotNil(execCtx, "execCtx")
	invariant.Precondition(step.Tree != nil, "step must have a tree")
	if isExecutionCanceled(execCtx) {
		return decorator.ExitCanceled
	}

	if logic, ok := step.Tree.(*planfmt.LogicNode); ok {
		return e.executePlanBlock(execCtx, logic.Block)
	}

	var stdin io.Reader
	var stdout io.Writer
	if ec, ok := execCtx.(*executionContext); ok {
		stdin = ec.stdin
		stdout = ec.stdoutPipe
	}

	return e.executePlanTreeIO(execCtx, step.Tree, stdin, stdout)
}

func (e *executor) executePlanBlock(execCtx sdk.ExecutionContext, steps []planfmt.Step) int {
	for _, step := range steps {
		if isExecutionCanceled(execCtx) {
			return decorator.ExitCanceled
		}
		exitCode := e.executePlanStep(execCtx, step)
		if exitCode != 0 {
			return exitCode
		}
	}
	return 0
}

func (e *executor) executePlanTreeIO(execCtx sdk.ExecutionContext, node planfmt.ExecutionNode, stdin io.Reader, stdout io.Writer) int {
	invariant.NotNil(execCtx, "execCtx")
	if isExecutionCanceled(execCtx) {
		return decorator.ExitCanceled
	}

	switch n := node.(type) {
	case *planfmt.CommandNode:
		return e.executePlanCommandWithPipes(execCtx, n, stdin, stdout)

	case *planfmt.PipelineNode:
		return e.executePlanPipelineIO(execCtx, n, stdin, stdout)

	case *planfmt.AndNode:
		leftExit := e.executePlanTreeIO(execCtx, n.Left, nil, stdout)
		if leftExit != 0 {
			return leftExit
		}
		return e.executePlanTreeIO(execCtx, n.Right, stdin, stdout)

	case *planfmt.OrNode:
		leftExit := e.executePlanTreeIO(execCtx, n.Left, nil, stdout)
		if leftExit == 0 || isExecutionCanceled(execCtx) {
			return leftExit
		}
		return e.executePlanTreeIO(execCtx, n.Right, stdin, stdout)

	case *planfmt.SequenceNode:
		var lastExit int
		for i, child := range n.Nodes {
			if isExecutionCanceled(execCtx) {
				return decorator.ExitCanceled
			}
			childStdin := io.Reader(nil)
			if i == len(n.Nodes)-1 {
				childStdin = stdin
			}
			lastExit = e.executePlanTreeIO(execCtx, child, childStdin, stdout)
		}
		return lastExit

	case *planfmt.RedirectNode:
		return e.executePlanRedirect(execCtx, n, stdin)

	case *planfmt.LogicNode:
		return e.executePlanBlock(execCtx, n.Block)

	case *planfmt.TryNode:
		_, _ = fmt.Fprintln(e.stderr, "Error: try/catch/finally execution is not implemented")
		return decorator.ExitFailure

	default:
		invariant.Invariant(false, "unknown planfmt.ExecutionNode type: %T", node)
		return decorator.ExitFailure
	}
}

func (e *executor) executePlanTreeNode(execCtx sdk.ExecutionContext, node planfmt.ExecutionNode, stdin io.Reader, stdout io.Writer) int {
	switch n := node.(type) {
	case *planfmt.CommandNode:
		return e.executePlanCommandWithPipes(execCtx, n, stdin, stdout)
	case *planfmt.RedirectNode:
		return e.executePlanRedirect(execCtx, n, stdin)
	default:
		invariant.Invariant(false, "invalid pipeline element type %T", node)
		return decorator.ExitFailure
	}
}

func (e *executor) executePlanPipelineIO(execCtx sdk.ExecutionContext, pipeline *planfmt.PipelineNode, initialStdin io.Reader, finalStdout io.Writer) int {
	invariant.NotNil(execCtx, "execCtx")
	invariant.NotNil(pipeline, "pipeline")
	if isExecutionCanceled(execCtx) {
		return decorator.ExitCanceled
	}
	numCommands := len(pipeline.Commands)
	invariant.Precondition(numCommands > 0, "pipeline must have at least one command")
	for i, node := range pipeline.Commands {
		invariant.Precondition(node != nil, "pipeline command %d cannot be nil", i)
		switch node.(type) {
		case *planfmt.CommandNode, *planfmt.RedirectNode:
			// valid pipeline node types
		default:
			invariant.Precondition(false, "invalid pipeline element type %T", node)
		}
	}

	if numCommands == 1 {
		return e.executePlanTreeNode(execCtx, pipeline.Commands[0], initialStdin, finalStdout)
	}

	pipeReaders := make([]*os.File, numCommands-1)
	pipeWriters := make([]*os.File, numCommands-1)
	for i := 0; i < numCommands-1; i++ {
		pr, pw, err := os.Pipe()
		if err != nil {
			for j := 0; j < i; j++ {
				_ = pipeReaders[j].Close()
				_ = pipeWriters[j].Close()
			}
			return decorator.ExitFailure
		}
		pipeReaders[i] = pr
		pipeWriters[i] = pw
	}

	exitCodes := make([]int, numCommands)
	pipeReaderCloseOnce := make([]sync.Once, numCommands-1)
	pipeWriterCloseOnce := make([]sync.Once, numCommands-1)

	var wg sync.WaitGroup
	wg.Add(numCommands)

	for i := 0; i < numCommands; i++ {
		cmdIndex := i
		node := pipeline.Commands[i]

		go func() {
			defer wg.Done()

			var stdin io.Reader
			if cmdIndex == 0 {
				stdin = initialStdin
			} else {
				stdin = pipeReaders[cmdIndex-1]
				defer func() {
					idx := cmdIndex - 1
					pipeReaderCloseOnce[idx].Do(func() {
						_ = pipeReaders[idx].Close()
					})
				}()
			}

			var stdout io.Writer
			if cmdIndex < numCommands-1 {
				stdout = pipeWriters[cmdIndex]
				defer func() {
					idx := cmdIndex
					pipeWriterCloseOnce[idx].Do(func() {
						_ = pipeWriters[idx].Close()
					})
				}()
			} else if finalStdout != nil {
				stdout = finalStdout
			} else {
				stdout = os.Stdout
			}

			exitCodes[cmdIndex] = e.executePlanTreeNode(execCtx, node, stdin, stdout)
		}()
	}

	wg.Wait()
	return exitCodes[numCommands-1]
}

func (e *executor) executePlanCommandWithPipes(execCtx sdk.ExecutionContext, cmd *planfmt.CommandNode, stdin io.Reader, stdout io.Writer) int {
	invariant.NotNil(execCtx, "execCtx")
	if isExecutionCanceled(execCtx) {
		return decorator.ExitCanceled
	}

	commandExecCtx := withExecutionTransport(execCtx, cmd.TransportID)

	params, ok := e.resolveCommandParams(commandExecCtx, cmd.Decorator, planArgsToMap(cmd.Args))
	if !ok {
		return decorator.ExitFailure
	}

	if isShellDecorator(cmd.Decorator) {
		return e.executeShellWithParams(commandExecCtx, params, stdin, stdout)
	}

	decoratorName := normalizeDecoratorName(cmd.Decorator)
	execDec, ok, reason := decorator.Global().GetExec(decoratorName)
	invariant.Invariant(ok, "%s", execLookupError(cmd.Decorator, reason))

	return e.executePlanDecorator(commandExecCtx, cmd, execDec, params, stdin, stdout)
}

func (e *executor) executePlanDecorator(
	execCtx sdk.ExecutionContext,
	cmd *planfmt.CommandNode,
	execDec decorator.Exec,
	params map[string]any,
	stdin io.Reader,
	stdout io.Writer,
) int {
	if isExecutionCanceled(execCtx) {
		return decorator.ExitCanceled
	}

	var next decorator.ExecNode
	if len(cmd.Block) > 0 {
		next = &planBlockNode{executor: e, execCtx: execCtx, steps: cmd.Block}
	}

	node := execDec.Wrap(next, params)
	if node == nil {
		if next == nil {
			return 0
		}
		node = next
	}

	transportID := transportIDForPlanDecoratorExecution(execCtx, cmd, execDec)
	baseSession, sessionErr := e.sessions.SessionFor(transportID)
	if sessionErr != nil {
		_, _ = fmt.Fprintf(e.stderr, "Error creating session: %v\n", sessionErr)
		return decorator.ExitFailure
	}
	session := sessionForExecutionContext(baseSession, execCtx)

	if stdout == nil {
		stdout = os.Stdout
	}

	decoratorExecCtx := decorator.ExecContext{
		Context: execCtx.Context(),
		Session: session,
		Stdin:   stdin,
		Stdout:  stdout,
		Stderr:  e.stderr,
		Trace:   nil,
	}

	result, err := node.Execute(decoratorExecCtx)
	if err != nil {
		_, _ = fmt.Fprintf(e.stderr, "Error: %v\n", err)
	}

	return result.ExitCode
}

func (e *executor) executePlanRedirect(execCtx sdk.ExecutionContext, redirect *planfmt.RedirectNode, stdin io.Reader) int {
	invariant.NotNil(execCtx, "execCtx")
	invariant.NotNil(redirect, "redirect node")
	if isExecutionCanceled(execCtx) {
		return decorator.ExitCanceled
	}

	redirectExecCtx := withExecutionTransport(execCtx, redirect.Target.TransportID)
	stderrOnly := planRedirectStderrEnabled(redirect.Source)
	transportID := executionTransportID(redirectExecCtx)

	params, ok := e.resolveCommandParams(redirectExecCtx, redirect.Target.Decorator, planArgsToMap(redirect.Target.Args))
	if !ok {
		return decorator.ExitFailure
	}
	ioDecorator, sinkIdentity, ok := resolvePlanIOSinkArgs(redirect.Target.Command(), params, e.stderr)
	if !ok {
		return decorator.ExitFailure
	}

	caps := ioDecorator.IOCaps()
	if redirect.Mode == planfmt.RedirectInput && !caps.Read {
		e.reportSinkError(sinkIdentity, "validate", transportID, fmt.Errorf("does not support input (<)"))
		return decorator.ExitFailure
	}
	if redirect.Mode == planfmt.RedirectOverwrite && !caps.Write {
		e.reportSinkError(sinkIdentity, "validate", transportID, fmt.Errorf("does not support overwrite (>)"))
		return decorator.ExitFailure
	}
	if redirect.Mode == planfmt.RedirectAppend && !caps.Append {
		e.reportSinkError(sinkIdentity, "validate", transportID, fmt.Errorf("does not support append (>>)"))
		return decorator.ExitFailure
	}

	baseSession, sessionErr := e.sessions.SessionFor(executionTransportID(redirectExecCtx))
	if sessionErr != nil {
		e.reportSinkError(sinkIdentity, "open", transportID, sessionErr)
		return decorator.ExitFailure
	}
	session := sessionForExecutionContext(baseSession, redirectExecCtx)
	if isExecutionCanceled(redirectExecCtx) {
		return decorator.ExitCanceled
	}

	decoratorCtx := decorator.ExecContext{
		Context: redirectExecCtx.Context(),
		Session: session,
		Stderr:  e.stderr,
	}

	if redirect.Mode == planfmt.RedirectInput {
		source, ok := ioDecorator.(decorator.Source)
		if !ok {
			e.reportSinkError(sinkIdentity, "validate", transportID, fmt.Errorf("endpoint does not implement input"))
			return decorator.ExitFailure
		}
		reader, err := source.OpenRead(decoratorCtx)
		if err != nil {
			e.reportSinkError(sinkIdentity, "open", transportID, err)
			return decorator.ExitFailure
		}

		exitCode := e.executePlanTreeIO(redirectExecCtx, withPlanRedirectedStderrSource(redirect.Source, stderrOnly), reader, nil)
		if closeErr := reader.Close(); closeErr != nil {
			e.reportSinkError(sinkIdentity, "close", transportID, closeErr)
			return decorator.ExitFailure
		}
		return exitCode
	}

	return e.runOutput(redirectExecCtx, sinkIdentity, transportID,
		func(ctx context.Context) (decorator.Output, error) {
			decoratorCtx.Context = ctx
			sink, ok := ioDecorator.(decorator.Sink)
			if !ok {
				return nil, fmt.Errorf("endpoint does not implement output")
			}
			return sink.OpenWrite(decoratorCtx, redirect.Mode == planfmt.RedirectAppend)
		}, func(ctx sdk.ExecutionContext, writer io.Writer) int {
			return e.executePlanTreeIO(ctx, withPlanRedirectedStderrSource(redirect.Source, stderrOnly), stdin, writer)
		})
}

func planRedirectStderrEnabled(node planfmt.ExecutionNode) bool {
	cmd, ok := node.(*planfmt.CommandNode)
	if !ok {
		return false
	}

	for _, arg := range cmd.Args {
		if arg.Key == "stderr" && arg.Val.Kind == planfmt.ValueBool {
			return arg.Val.Bool
		}
	}

	return false
}

func withPlanRedirectedStderrSource(node planfmt.ExecutionNode, stderrOnly bool) planfmt.ExecutionNode {
	if !stderrOnly {
		return node
	}

	cmd, ok := node.(*planfmt.CommandNode)
	if !ok || normalizeDecoratorName(cmd.Decorator) != "shell" {
		return node
	}

	args := make([]planfmt.Arg, len(cmd.Args))
	copy(args, cmd.Args)

	for i := range args {
		if args[i].Key != "command" || args[i].Val.Kind != planfmt.ValueString || args[i].Val.Str == "" {
			continue
		}

		args[i].Val.Str = "(" + args[i].Val.Str + ") 3>&1 1>&2 2>&3 3>&-"
		return &planfmt.CommandNode{
			Decorator:   cmd.Decorator,
			TransportID: cmd.TransportID,
			Args:        args,
			Block:       cmd.Block,
		}
	}

	return node
}

func isExecutionCanceled(execCtx sdk.ExecutionContext) bool {
	if execCtx == nil {
		return false
	}
	ctx := execCtx.Context()
	if ctx == nil {
		return false
	}
	return ctx.Err() != nil
}

func sourceTransportIDForPlan(node planfmt.ExecutionNode) string {
	if node == nil {
		return ""
	}

	switch n := node.(type) {
	case *planfmt.CommandNode:
		return n.TransportID
	case *planfmt.PipelineNode:
		if len(n.Commands) == 0 {
			return ""
		}
		return sourceTransportIDForPlan(n.Commands[0])
	case *planfmt.RedirectNode:
		return sourceTransportIDForPlan(n.Source)
	case *planfmt.AndNode:
		return sourceTransportIDForPlan(n.Left)
	case *planfmt.OrNode:
		return sourceTransportIDForPlan(n.Left)
	case *planfmt.SequenceNode:
		if len(n.Nodes) == 0 {
			return ""
		}
		return sourceTransportIDForPlan(n.Nodes[0])
	default:
		return ""
	}
}

func transportIDForPlanDecoratorExecution(execCtx sdk.ExecutionContext, cmd *planfmt.CommandNode, execDec decorator.Exec) string {
	transportID := executionTransportID(execCtx)
	if cmd == nil || len(cmd.Block) == 0 {
		return transportID
	}

	transportDec, ok := execDec.(decorator.Transport)
	if !ok || !transportDec.MaterializeSession() {
		return transportID
	}

	blockTransportID := sourceTransportIDForPlan(cmd.Block[0].Tree)
	if blockTransportID == "" {
		return transportID
	}

	return normalizedTransportID(blockTransportID)
}

type planBlockNode struct {
	executor *executor
	execCtx  sdk.ExecutionContext
	steps    []planfmt.Step
}

func (n *planBlockNode) Execute(ctx decorator.ExecContext) (decorator.Result, error) {
	child := childExecutionContextFromDecorator(n.execCtx, ctx)
	exitCode := n.executor.executePlanBlock(child, n.steps)
	return decorator.Result{ExitCode: exitCode}, nil
}

func (n *planBlockNode) BranchCount() int {
	return len(n.steps)
}

func (n *planBlockNode) ExecuteBranch(index int, ctx decorator.ExecContext) (decorator.Result, error) {
	invariant.Precondition(index >= 0 && index < len(n.steps), "branch index out of bounds: %d", index)

	child := childExecutionContextFromDecorator(n.execCtx, ctx)
	exitCode := n.executor.executePlanStep(child, n.steps[index])
	return decorator.Result{ExitCode: exitCode}, nil
}

func normalizeDecoratorName(name string) string {
	if name != "" && name[0] == '@' {
		return name[1:]
	}
	return name
}

func resolvePlanIOSink(target *planfmt.CommandNode, stderr io.Writer) (decorator.IO, string, bool) {
	return resolvePlanIOSinkArgs(target, planArgsToMap(target.Args), stderr)
}

func resolvePlanIOSinkArgs(target *planfmt.CommandNode, args map[string]any, stderr io.Writer) (decorator.IO, string, bool) {
	if target == nil {
		_, _ = fmt.Fprintln(stderr, "Error: redirect target is nil")
		return nil, "", false
	}

	decoratorPath := normalizeDecoratorName(target.Decorator)
	ioDecorator, ok, reason := decorator.Global().GetRedirectTarget(decoratorPath)
	if !ok {
		if reason != "" {
			_, _ = fmt.Fprintf(stderr, "Error: %s\n", reason)
			return nil, target.Decorator, false
		}
		_, _ = fmt.Fprintf(stderr, "Error: unknown redirect sink decorator: %s\n", target.Decorator)
		return nil, target.Decorator, false
	}

	if factory, ok := ioDecorator.(decorator.IOFactory); ok {
		ioDecorator = factory.WithParams(args)
	}

	identity := target.Decorator
	if decoratorPath == "file" {
		if path, ok := planArgsToMap(target.Args)["path"].(string); ok && path != "" {
			identity = "@file(" + path + ")"
		}
		return ioDecorator, identity, true
	}

	if commandPath, ok := args["command"].(string); ok && commandPath != "" {
		identity = identity + "(" + commandPath + ")"
	}

	return ioDecorator, identity, true
}

func planArgsToMap(args []planfmt.Arg) map[string]any {
	out := make(map[string]any, len(args))
	for _, arg := range args {
		out[arg.Key] = planValueToAny(arg.Val)
	}
	return out
}

func planValueToAny(val planfmt.Value) any {
	switch val.Kind {
	case planfmt.ValueString:
		return val.Str
	case planfmt.ValueInt:
		return val.Int
	case planfmt.ValueBool:
		return val.Bool
	case planfmt.ValueFloat:
		return val.Float
	case planfmt.ValueDuration:
		return val.Duration
	case planfmt.ValueArray:
		items := make([]any, len(val.Array))
		for i, item := range val.Array {
			items[i] = planValueToAny(item)
		}
		return items
	case planfmt.ValueMap:
		mapped := make(map[string]any, len(val.Map))
		for key, item := range val.Map {
			mapped[key] = planValueToAny(item)
		}
		return mapped
	case planfmt.ValuePlaceholder:
		invariant.Invariant(false, "ValuePlaceholder reached execution runtime (ref=%d)", val.Ref)
		return fmt.Sprintf("$%d", val.Ref)
	default:
		return nil
	}
}
