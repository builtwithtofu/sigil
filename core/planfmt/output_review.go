package planfmt

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// ReviewOutputs reports replay risks and rejects identical declared file targets
// in concurrent branches. It does not resolve aliases or inspect any filesystem.
func ReviewOutputs(plan *Plan) ([]string, error) {
	review := outputReview{transports: make(map[string]Transport, len(plan.Transports))}
	for _, transport := range plan.Transports {
		review.transports[transport.ID] = transport
	}
	_, err := review.steps(plan.Steps, outputScope{}, false)
	return review.warnings, err
}

type outputScope struct {
	transport string
	directory string
	retry     bool
}
type outputKey struct{ transport, directory, path string }
type outputReview struct {
	warnings   []string
	transports map[string]Transport
}

func (r *outputReview) inTransport(scope outputScope, id string) outputScope {
	if scope.transport != id {
		scope.transport = id
		scope.directory = outputStringArg(r.transports[id].Args, "cwd")
	}
	return scope
}

func (r *outputReview) localTransport(id string) bool {
	return id == "" || id == "local" || r.transports[id].Decorator == "local"
}

func (r *outputReview) steps(steps []Step, scope outputScope, parallel bool) (map[outputKey]struct{}, error) {
	nodes := make([]ExecutionNode, 0, len(steps))
	for _, step := range steps {
		nodes = append(nodes, step.Tree)
	}
	return r.nodes(nodes, scope, parallel)
}
func (r *outputReview) nodes(nodes []ExecutionNode, scope outputScope, parallel bool) (map[outputKey]struct{}, error) {
	outputs := map[outputKey]struct{}{}
	for _, node := range nodes {
		child, err := r.node(node, scope)
		if err != nil {
			return nil, err
		}
		keys := make([]outputKey, 0, len(child))
		for key := range child {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, b := keys[i], keys[j]
			if a.transport != b.transport {
				return a.transport < b.transport
			}
			if a.directory != b.directory {
				return a.directory < b.directory
			}
			return a.path < b.path
		})
		for _, key := range keys {
			if _, exists := outputs[key]; exists && parallel {
				return nil, fmt.Errorf("parallel branches write the same declared destination @file(%q) in context %s; use distinct destinations (this check does not detect aliases or external writers)", key.path, key.transport)
			}
			outputs[key] = struct{}{}
		}
	}
	return outputs, nil
}
func (r *outputReview) node(node ExecutionNode, scope outputScope) (map[outputKey]struct{}, error) {
	switch n := node.(type) {
	case *CommandNode:
		scope = r.inTransport(scope, n.TransportID)
		if n.Decorator == "@exec.retry" {
			scope.retry = true
		}
		if n.Decorator == "@fs.workdir" {
			dir := outputStringArg(n.Args, "path")
			if r.localTransport(scope.transport) && !filepath.IsAbs(dir) {
				scope.directory = filepath.Join(scope.directory, dir)
			} else {
				// Remote workdirs are relative to that session's initial directory,
				// not the controller's cwd. Keep unknown bases relative.
				scope.directory = dir
			}
		}
		return r.steps(n.Block, scope, n.Decorator == "@exec.parallel")
	case *RedirectNode:
		scope = r.inTransport(scope, n.Target.TransportID)
		outputs, err := r.node(n.Source, scope)
		if err != nil {
			return nil, err
		}
		if n.Mode == RedirectInput {
			return outputs, nil
		}
		destination := outputStringArg(n.Target.Args, "path")
		if scope.retry && n.Mode == RedirectAppend {
			r.warnings = append(r.warnings, "retrying append can duplicate output: "+n.Target.Decorator+"("+destination+"); retries are not exactly-once")
		}
		// Identical opaque references also identify the same declared destination;
		// different declarations might still alias, which this check cannot prove.
		if n.Target.Decorator == "@file" && destination != "" {
			directory := scope.directory
			if strings.HasPrefix(destination, "/") || (r.localTransport(scope.transport) && filepath.IsAbs(destination)) {
				directory = ""
			}
			outputs[outputKey{n.Target.TransportID, directory, destination}] = struct{}{}
		}
		return outputs, nil
	case *PipelineNode:
		return r.nodes(n.Commands, scope, true)
	case *SequenceNode:
		return r.nodes(n.Nodes, scope, false)
	case *AndNode:
		return r.nodes([]ExecutionNode{n.Left, n.Right}, scope, false)
	case *OrNode:
		return r.nodes([]ExecutionNode{n.Left, n.Right}, scope, false)
	case *LogicNode:
		return r.steps(n.Block, scope, false)
	case *TryNode:
		nodes := []ExecutionNode{}
		for _, steps := range [][]Step{n.TryBlock, n.CatchBlock, n.FinallyBlock} {
			for _, step := range steps {
				nodes = append(nodes, step.Tree)
			}
		}
		return r.nodes(nodes, scope, false)
	default:
		return map[outputKey]struct{}{}, nil
	}
}
func outputStringArg(args []Arg, key string) string {
	for _, arg := range args {
		if arg.Key == key && arg.Val.Kind == ValueString {
			return arg.Val.Str
		}
	}
	return ""
}
