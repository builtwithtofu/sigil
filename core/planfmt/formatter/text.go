// Package formatter provides human-readable formatting for plans.
// This includes text output, diffs, and tree displays.
package formatter

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/builtwithtofu/sigil/core/planfmt"
)

// Format returns a human-readable text representation of the plan.
// Used for contract files and inspection.
//
// Format:
//
//	target: <name>
//	step 1: <formatted step>
//	step 2: <formatted step>
func Format(plan *planfmt.Plan) string {
	var b strings.Builder

	// Write target
	fmt.Fprintf(&b, "target: %s\n", plan.Target)

	// Write each step
	for i, step := range plan.Steps {
		fmt.Fprintf(&b, "step %d: %s\n", i+1, FormatStep(&step))
	}
	formatOutputReview(&b, plan)

	return b.String()
}

// FormatStep returns a single step as text by formatting its execution tree.
func FormatStep(step *planfmt.Step) string {
	return formatExecutionNode(step.Tree)
}

// formatExecutionNode formats an execution node to text
func formatExecutionNode(node planfmt.ExecutionNode) string {
	switch n := node.(type) {
	case *planfmt.CommandNode:
		return formatCommandNode(n)
	case *planfmt.PipelineNode:
		var parts []string
		for _, elem := range n.Commands {
			// Pipeline elements can be CommandNode or RedirectNode
			parts = append(parts, formatExecutionNode(elem))
		}
		return strings.Join(parts, " | ")
	case *planfmt.AndNode:
		return fmt.Sprintf("%s && %s", formatExecutionNode(n.Left), formatExecutionNode(n.Right))
	case *planfmt.OrNode:
		return fmt.Sprintf("%s || %s", formatExecutionNode(n.Left), formatExecutionNode(n.Right))
	case *planfmt.SequenceNode:
		var parts []string
		for _, child := range n.Nodes {
			parts = append(parts, formatExecutionNode(child))
		}
		return strings.Join(parts, " ; ")
	case *planfmt.RedirectNode:
		op := ">"
		if n.Mode == planfmt.RedirectAppend {
			op = ">>"
		}
		return fmt.Sprintf("%s %s %s", formatExecutionNode(n.Source), op, formatEndpoint(n.Target))
	case *planfmt.LogicNode:
		return formatLogicNode(n)
	case *planfmt.TryNode:
		return formatTryNode(n)
	default:
		return fmt.Sprintf("(unknown: %T)", node)
	}
}

// formatTryNode formats a try/catch/finally node
func formatTryNode(try *planfmt.TryNode) string {
	var parts []string
	parts = append(parts, "try {")
	for _, step := range try.TryBlock {
		parts = append(parts, "  "+formatExecutionNode(step.Tree))
	}
	parts = append(parts, "}")
	if len(try.CatchBlock) > 0 {
		parts = append(parts, "catch {")
		for _, step := range try.CatchBlock {
			parts = append(parts, "  "+formatExecutionNode(step.Tree))
		}
		parts = append(parts, "}")
	}
	if len(try.FinallyBlock) > 0 {
		parts = append(parts, "finally {")
		for _, step := range try.FinallyBlock {
			parts = append(parts, "  "+formatExecutionNode(step.Tree))
		}
		parts = append(parts, "}")
	}
	return strings.Join(parts, " ")
}

// formatCommandNode formats a single command node
func formatCommandNode(cmd *planfmt.CommandNode) string {
	// Special case: @shell with single "command" arg - show command directly
	if cmd.Decorator == "@shell" && len(cmd.Args) == 1 && cmd.Args[0].Key == "command" {
		return fmt.Sprintf("@shell %s", cmd.Args[0].Val.Str)
	}

	// General case: decorator with args
	if len(cmd.Args) == 0 {
		return cmd.Decorator
	}

	// Format args as key=value pairs
	var argParts []string
	for _, arg := range cmd.Args {
		argParts = append(argParts, fmt.Sprintf("%s=%s", arg.Key, formatValue(&arg.Val)))
	}

	return fmt.Sprintf("%s(%s)", cmd.Decorator, strings.Join(argParts, ", "))
}

func formatLogicNode(logic *planfmt.LogicNode) string {
	if logic == nil {
		return ""
	}

	if logic.Kind == "call" {
		if logic.Condition != "" {
			return logic.Condition
		}
		return "()"
	}

	if logic.Condition != "" && logic.Result != "" {
		return fmt.Sprintf("%s %s -> %s", logic.Kind, logic.Condition, logic.Result)
	}
	if logic.Condition != "" {
		return fmt.Sprintf("%s %s", logic.Kind, logic.Condition)
	}
	if logic.Result != "" {
		return fmt.Sprintf("%s %s", logic.Kind, logic.Result)
	}
	return logic.Kind
}

// formatValue formats a value based on its kind
func formatValue(val *planfmt.Value) string {
	switch val.Kind {
	case planfmt.ValueString:
		return val.Str
	case planfmt.ValueInt:
		return fmt.Sprintf("%d", val.Int)
	case planfmt.ValueBool:
		return fmt.Sprintf("%t", val.Bool)
	case planfmt.ValuePlaceholder:
		return fmt.Sprintf("$%d", val.Ref)
	case planfmt.ValueFloat:
		return strconv.FormatFloat(val.Float, 'g', -1, 64)
	case planfmt.ValueDuration:
		return val.Duration
	case planfmt.ValueArray:
		parts := make([]string, 0, len(val.Array))
		for i := range val.Array {
			item := val.Array[i]
			parts = append(parts, formatValue(&item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case planfmt.ValueMap:
		keys := make([]string, 0, len(val.Map))
		for key := range val.Map {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			item := val.Map[key]
			parts = append(parts, fmt.Sprintf("%s=%s", key, formatValue(&item)))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		return "?"
	}
}
