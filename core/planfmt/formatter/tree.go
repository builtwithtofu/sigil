package formatter

import (
	"fmt"
	"io"
	"strings"

	"github.com/builtwithtofu/sigil/core/planfmt"
)

// ANSI color codes
const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorCyan   = "\033[36m"
	ColorGray   = "\033[90m"
)

// Colorize wraps text in ANSI color codes if color is enabled
func Colorize(text, color string, useColor bool) string {
	if !useColor {
		return text
	}
	return color + text + ColorReset
}

// FormatTree renders a plan as a tree structure to the given writer.
// This is used for --dry-run output to show the execution plan visually.
func FormatTree(w io.Writer, plan *planfmt.Plan, useColor bool) {
	// Print target name
	_, _ = fmt.Fprintf(w, "%s:\n", plan.Target)

	// Handle empty plan
	if len(plan.Steps) == 0 {
		_, _ = fmt.Fprintf(w, "(no steps)\n")
		return
	}

	renderStepList(w, plan.Steps, "", useColor)
	formatOutputReview(w, plan)
}

func renderStepList(w io.Writer, steps []planfmt.Step, indent string, useColor bool) {
	for i := 0; i < len(steps); {
		step := steps[i]
		if logic, ok := step.Tree.(*planfmt.LogicNode); ok && logic.Kind == "for" {
			cond := logic.Condition
			j := i
			for j < len(steps) {
				nextLogic, ok := steps[j].Tree.(*planfmt.LogicNode)
				if !ok || nextLogic.Kind != "for" || nextLogic.Condition != cond {
					break
				}
				j++
			}
			isLastGroup := j == len(steps)
			renderForGroup(w, steps[i:j], indent, isLastGroup, useColor)
			i = j
			continue
		}

		isLast := i == len(steps)-1
		renderTreeStep(w, step, indent, isLast, useColor)
		i++
	}
}

func renderForGroup(w io.Writer, steps []planfmt.Step, indent string, isLast, useColor bool) {
	if len(steps) == 0 {
		return
	}

	logic, ok := steps[0].Tree.(*planfmt.LogicNode)
	if !ok {
		return
	}

	condition := logic.Condition
	if condition == "" {
		condition = "?"
	}

	var prefix string
	if isLast {
		prefix = indent + "└─ "
	} else {
		prefix = indent + "├─ "
	}

	_, _ = fmt.Fprintf(w, "%sfor %s: %d iterations\n", prefix, condition, len(steps))

	iterationIndent := indent
	if isLast {
		iterationIndent += "   "
	} else {
		iterationIndent += "│  "
	}

	for i, step := range steps {
		iterLogic, ok := step.Tree.(*planfmt.LogicNode)
		if !ok {
			continue
		}

		iterPrefix := iterationIndent
		if i == len(steps)-1 {
			iterPrefix += "└─ "
		} else {
			iterPrefix += "├─ "
		}

		label := formatForIterationLabel(iterLogic.Result, i+1)
		block := iterLogic.Block
		if len(block) == 1 && canInlineIterationStep(block[0]) {
			inline := renderExecutionNode(block[0].Tree, useColor)
			_, _ = fmt.Fprintf(w, "%s[%d] %s: %s\n", iterPrefix, i+1, label, inline)
			continue
		}

		if len(block) == 0 {
			_, _ = fmt.Fprintf(w, "%s[%d] %s\n", iterPrefix, i+1, label)
			continue
		}

		_, _ = fmt.Fprintf(w, "%s[%d] %s:\n", iterPrefix, i+1, label)
		nestedIndent := iterationIndent
		if i == len(steps)-1 {
			nestedIndent += "   "
		} else {
			nestedIndent += "│  "
		}
		renderStepList(w, block, nestedIndent, useColor)
	}
}

func formatForIterationLabel(result string, index int) string {
	result = strings.TrimSpace(result)
	if result == "" {
		return fmt.Sprintf("iteration %d", index)
	}
	const marker = " (iteration "
	if strings.HasSuffix(result, ")") {
		if start := strings.LastIndex(result, marker); start != -1 {
			return strings.TrimSpace(result[:start])
		}
	}
	return result
}

func canInlineIterationStep(step planfmt.Step) bool {
	if step.Tree == nil {
		return false
	}
	if cmd, ok := step.Tree.(*planfmt.CommandNode); ok {
		return len(cmd.Block) == 0
	}
	if logic, ok := step.Tree.(*planfmt.LogicNode); ok {
		return len(logic.Block) == 0
	}
	if _, ok := step.Tree.(*planfmt.TryNode); ok {
		return false
	}
	return true
}

// renderTreeStep renders a single step with tree characters
func renderTreeStep(w io.Writer, step planfmt.Step, indent string, isLast, useColor bool) {
	// Choose tree character
	var prefix string
	if isLast {
		prefix = indent + "└─ "
	} else {
		prefix = indent + "├─ "
	}

	// Handle TryNode specially to render its nested blocks
	if try, ok := step.Tree.(*planfmt.TryNode); ok {
		renderTryBlock(w, try, indent, isLast, useColor)
		return
	}

	// Render the execution tree
	treeStr := renderExecutionNode(step.Tree, useColor)
	_, _ = fmt.Fprintf(w, "%s%s\n", prefix, treeStr)

	// Render nested blocks if this is a CommandNode with a Block
	if cmd, ok := step.Tree.(*planfmt.CommandNode); ok && len(cmd.Block) > 0 {
		nestedIndent := indent
		if isLast {
			nestedIndent += "   "
		} else {
			nestedIndent += "│  "
		}
		renderStepList(w, cmd.Block, nestedIndent, useColor)
	}

	if logic, ok := step.Tree.(*planfmt.LogicNode); ok && len(logic.Block) > 0 {
		nestedIndent := indent
		if isLast {
			nestedIndent += "   "
		} else {
			nestedIndent += "│  "
		}
		renderStepList(w, logic.Block, nestedIndent, useColor)
	}
}

// renderTryBlock renders a try/catch/finally block with proper indentation
func renderTryBlock(w io.Writer, try *planfmt.TryNode, indent string, isLast, useColor bool) {
	var prefix string
	if isLast {
		prefix = indent + "└─ "
	} else {
		prefix = indent + "├─ "
	}
	_, _ = fmt.Fprintf(w, "%s%s\n", prefix, Colorize("try {", ColorYellow, useColor))

	blockIndent := indent
	if isLast {
		blockIndent += "   "
	} else {
		blockIndent += "│  "
	}

	renderStepList(w, try.TryBlock, blockIndent, useColor)
	_, _ = fmt.Fprintf(w, "%s%s\n", blockIndent, Colorize("}", ColorYellow, useColor))

	if len(try.CatchBlock) > 0 {
		_, _ = fmt.Fprintf(w, "%s%s\n", blockIndent, Colorize("catch {", ColorYellow, useColor))
		renderStepList(w, try.CatchBlock, blockIndent, useColor)
		_, _ = fmt.Fprintf(w, "%s%s\n", blockIndent, Colorize("}", ColorYellow, useColor))
	}

	if len(try.FinallyBlock) > 0 {
		_, _ = fmt.Fprintf(w, "%s%s\n", blockIndent, Colorize("finally {", ColorYellow, useColor))
		renderStepList(w, try.FinallyBlock, blockIndent, useColor)
		_, _ = fmt.Fprintf(w, "%s%s\n", blockIndent, Colorize("}", ColorYellow, useColor))
	}
}

// renderExecutionNode renders an execution node to a string
func renderExecutionNode(node planfmt.ExecutionNode, useColor bool) string {
	switch n := node.(type) {
	case *planfmt.CommandNode:
		return renderCommandNode(n, useColor)
	case *planfmt.PipelineNode:
		return renderPipelineNode(n, useColor)
	case *planfmt.AndNode:
		return renderAndNode(n, useColor)
	case *planfmt.OrNode:
		return renderOrNode(n, useColor)
	case *planfmt.SequenceNode:
		return renderSequenceNode(n, useColor)
	case *planfmt.RedirectNode:
		return renderRedirectNode(n, useColor)
	case *planfmt.LogicNode:
		return renderLogicNode(n)
	case *planfmt.TryNode:
		return renderTryNode(n, useColor)
	default:
		return fmt.Sprintf("(unknown node type: %T)", node)
	}
}

// renderTryNode renders a try/catch/finally node for inline display
func renderTryNode(try *planfmt.TryNode, useColor bool) string {
	return Colorize("try { ... }", ColorYellow, useColor)
}

// renderCommandNode renders a single command
func renderCommandNode(cmd *planfmt.CommandNode, useColor bool) string {
	decorator := Colorize(cmd.Decorator, ColorBlue, useColor)
	commandStr := getCommandString(cmd)
	return fmt.Sprintf("%s %s", decorator, commandStr)
}

// renderPipelineNode renders a pipeline (cmd1 | cmd2 | cmd3)
func renderPipelineNode(pipe *planfmt.PipelineNode, useColor bool) string {
	var parts []string
	for _, elem := range pipe.Commands {
		// Pipeline elements can be CommandNode or RedirectNode
		parts = append(parts, renderExecutionNode(elem, useColor))
	}
	return strings.Join(parts, " | ")
}

// renderAndNode renders an AND node (left && right)
func renderAndNode(and *planfmt.AndNode, useColor bool) string {
	left := renderExecutionNode(and.Left, useColor)
	right := renderExecutionNode(and.Right, useColor)
	return fmt.Sprintf("%s && %s", left, right)
}

// renderOrNode renders an OR node (left || right)
func renderOrNode(or *planfmt.OrNode, useColor bool) string {
	left := renderExecutionNode(or.Left, useColor)
	right := renderExecutionNode(or.Right, useColor)
	return fmt.Sprintf("%s || %s", left, right)
}

// renderSequenceNode renders a sequence node (node1 ; node2 ; node3)
func renderSequenceNode(seq *planfmt.SequenceNode, useColor bool) string {
	var parts []string
	for _, node := range seq.Nodes {
		parts = append(parts, renderExecutionNode(node, useColor))
	}
	return strings.Join(parts, " ; ")
}

func renderRedirectNode(redirect *planfmt.RedirectNode, useColor bool) string {
	op := ">"
	if redirect.Mode == planfmt.RedirectAppend {
		op = ">>"
	}
	return fmt.Sprintf("%s %s %s", renderExecutionNode(redirect.Source, useColor), op, formatEndpoint(redirect.Target))
}

func renderLogicNode(logic *planfmt.LogicNode) string {
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

// getCommandString extracts the command string from a CommandNode for display
func getCommandString(cmd *planfmt.CommandNode) string {
	// For @shell decorator, look for "command" arg
	for _, arg := range cmd.Args {
		if arg.Key == "command" && arg.Val.Kind == planfmt.ValueString {
			return arg.Val.Str
		}
	}
	// Fallback: show all args with proper value formatting
	var parts []string
	for _, arg := range cmd.Args {
		parts = append(parts, fmt.Sprintf("%s=%s", arg.Key, formatValue(&arg.Val)))
	}
	return strings.Join(parts, " ")
}
