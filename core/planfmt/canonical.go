package planfmt

import (
	"crypto/sha256"
	"fmt"
	"sort"

	"github.com/fxamacker/cbor/v2"
)

// CanonicalPlan is the intermediate form for deterministic hashing.
// It uses placeholders instead of DisplayIDs to break the circular dependency
// between DisplayID generation and plan hash computation.
//
// Includes Target to ensure different commands (deploy vs destroy) produce
// different hashes, maintaining unlinkability across semantically different plans.
//
// Two-pass canonicalization:
//  1. Build canonical form with placeholders
//  2. Compute plan hash from canonical form
//  3. Generate DisplayIDs using plan_hash
//  4. Substitute DisplayIDs into plan
type CanonicalPlan struct {
	Version    uint8                // Canonical format version (for forward compatibility)
	Target     string               // Command/function being executed (ensures deploy != destroy)
	Steps      []CanonicalStep      // Steps in canonical form
	Transports []CanonicalTransport // Transport table in canonical form
	SecretUses []CanonicalSecretUse // Secret uses in canonical form
}

// CanonicalStep represents a step in canonical form
type CanonicalStep struct {
	ID   uint64
	Tree CanonicalNode
}

// CanonicalSecretUse represents a secret use in canonical form.
// Includes DisplayID and Site for contract verification.
// SiteID is derived from Site, so it's redundant for hashing.
type CanonicalSecretUse struct {
	DisplayID string // Secret identifier (e.g., "sigil:3J98t56A")
	Site      string // Human-readable path (e.g., "root/step-1/params/command")
}

// CanonicalNode is a union type for execution tree nodes in canonical form
type CanonicalNode struct {
	Type string // "command", "pipeline", "and", "or", "sequence", "redirect", "logic"

	// CommandNode fields
	Decorator   string
	TransportID string
	Args        []CanonicalArg
	Block       []CanonicalStep

	// PipelineNode fields
	Commands []CanonicalNode

	// AndNode/OrNode fields
	Left  *CanonicalNode
	Right *CanonicalNode

	// SequenceNode fields
	Nodes []CanonicalNode

	// RedirectNode fields
	Source *CanonicalNode
	Target *CanonicalNode
	Mode   int

	// LogicNode fields
	LogicKind string
	Condition string
	Result    string
}

// CanonicalArg represents an argument in canonical form
type CanonicalArg struct {
	Key   string
	Value CanonicalValue
}

// CanonicalValue represents a Value in canonical form.
// Uses slices for maps to ensure deterministic ordering.
type CanonicalValue struct {
	Kind     uint8
	Str      string
	Int      int64
	Bool     bool
	Ref      uint32
	Float    float64
	Duration string
	Array    []CanonicalValue
	Map      []CanonicalMapEntry
}

// CanonicalMapEntry represents a map key/value pair in canonical form.
type CanonicalMapEntry struct {
	Key   string
	Value CanonicalValue
}

// CanonicalTransport represents a transport entry in canonical form.
type CanonicalTransport struct {
	ID        string
	Decorator string
	Args      []CanonicalArg
	ParentID  string
}

// Canonicalize converts a Plan into canonical form for deterministic hashing.
// Sorts args before canonicalization to ensure same structure produces same hash.
// Includes Target to ensure different commands produce different hashes.
func (p *Plan) Canonicalize() (*CanonicalPlan, error) {
	// Sort args first to ensure deterministic ordering
	// Args may come from Go maps with non-deterministic iteration order
	p.sortArgs()
	p.sortTransports()

	cp := &CanonicalPlan{
		Version:    2,        // Endpoint execution semantics version.
		Target:     p.Target, // Include target to distinguish deploy vs destroy
		Steps:      make([]CanonicalStep, len(p.Steps)),
		Transports: make([]CanonicalTransport, len(p.Transports)),
		SecretUses: make([]CanonicalSecretUse, len(p.SecretUses)),
	}

	// Canonicalize steps
	for i := range p.Steps {
		cs, err := canonicalizeStep(&p.Steps[i])
		if err != nil {
			return nil, fmt.Errorf("step %d: %w", i, err)
		}
		cp.Steps[i] = cs
	}

	// Canonicalize secret uses (sorted by DisplayID then Site for determinism)
	// SiteID is omitted because it's derived from Site via HMAC (redundant for hashing)
	secretUses := make([]CanonicalSecretUse, len(p.SecretUses))
	for i := range p.SecretUses {
		secretUses[i] = CanonicalSecretUse{
			DisplayID: p.SecretUses[i].DisplayID,
			Site:      p.SecretUses[i].Site,
		}
	}
	sort.Slice(secretUses, func(i, j int) bool {
		if secretUses[i].DisplayID != secretUses[j].DisplayID {
			return secretUses[i].DisplayID < secretUses[j].DisplayID
		}
		return secretUses[i].Site < secretUses[j].Site
	})
	cp.SecretUses = secretUses

	// Canonicalize transports (sorted by ID for determinism)
	for i := range p.Transports {
		cp.Transports[i] = canonicalizeTransport(&p.Transports[i])
	}

	sort.Slice(cp.Transports, func(i, j int) bool {
		return cp.Transports[i].ID < cp.Transports[j].ID
	})

	return cp, nil
}

// canonicalizeStep converts a Step into canonical form
func canonicalizeStep(s *Step) (CanonicalStep, error) {
	cs := CanonicalStep{
		ID: s.ID,
	}

	if s.Tree != nil {
		node, err := toCanonicalNode(s.Tree)
		if err != nil {
			return cs, err
		}
		cs.Tree = node
	}

	return cs, nil
}

// toCanonicalNode converts an ExecutionNode into canonical form
func toCanonicalNode(node ExecutionNode) (CanonicalNode, error) {
	switch n := node.(type) {
	case *CommandNode:
		return canonicalizeCommandNode(n)
	case *PipelineNode:
		return canonicalizePipelineNode(n)
	case *AndNode:
		return canonicalizeAndNode(n)
	case *OrNode:
		return canonicalizeOrNode(n)
	case *SequenceNode:
		return canonicalizeSequenceNode(n)
	case *RedirectNode:
		return canonicalizeRedirectNode(n)
	case *LogicNode:
		return canonicalizeLogicNode(n)
	default:
		return CanonicalNode{}, fmt.Errorf("unknown node type: %T", node)
	}
}

// canonicalizeCommandNode converts a CommandNode into canonical form
func canonicalizeCommandNode(n *CommandNode) (CanonicalNode, error) {
	cn := CanonicalNode{
		Type:        "command",
		Decorator:   n.Decorator,
		TransportID: n.TransportID,
		Args:        make([]CanonicalArg, len(n.Args)),
		Block:       make([]CanonicalStep, len(n.Block)),
	}

	// Canonicalize args (already sorted by Key in Plan.sortArgs())
	for i := range n.Args {
		cn.Args[i] = CanonicalArg{
			Key:   n.Args[i].Key,
			Value: canonicalizeValue(n.Args[i].Val),
		}
	}

	// Canonicalize block steps
	for i := range n.Block {
		cs, err := canonicalizeStep(&n.Block[i])
		if err != nil {
			return cn, fmt.Errorf("block step %d: %w", i, err)
		}
		cn.Block[i] = cs
	}

	return cn, nil
}

func canonicalizeTransport(t *Transport) CanonicalTransport {
	ct := CanonicalTransport{
		ID:        t.ID,
		Decorator: t.Decorator,
		Args:      make([]CanonicalArg, len(t.Args)),
		ParentID:  t.ParentID,
	}

	for i := range t.Args {
		ct.Args[i] = CanonicalArg{
			Key:   t.Args[i].Key,
			Value: canonicalizeValue(t.Args[i].Val),
		}
	}

	return ct
}

func canonicalizeValue(val Value) CanonicalValue {
	canonical := CanonicalValue{
		Kind:     uint8(val.Kind),
		Str:      val.Str,
		Int:      val.Int,
		Bool:     val.Bool,
		Ref:      val.Ref,
		Float:    val.Float,
		Duration: val.Duration,
	}

	switch val.Kind {
	case ValueArray:
		canonical.Array = make([]CanonicalValue, len(val.Array))
		for i, item := range val.Array {
			canonical.Array[i] = canonicalizeValue(item)
		}
	case ValueMap:
		keys := make([]string, 0, len(val.Map))
		for key := range val.Map {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		canonical.Map = make([]CanonicalMapEntry, 0, len(keys))
		for _, key := range keys {
			canonical.Map = append(canonical.Map, CanonicalMapEntry{
				Key:   key,
				Value: canonicalizeValue(val.Map[key]),
			})
		}
	}

	return canonical
}

// canonicalizePipelineNode converts a PipelineNode into canonical form
func canonicalizePipelineNode(n *PipelineNode) (CanonicalNode, error) {
	cn := CanonicalNode{
		Type:     "pipeline",
		Commands: make([]CanonicalNode, len(n.Commands)),
	}

	for i := range n.Commands {
		cmd, err := toCanonicalNode(n.Commands[i])
		if err != nil {
			return cn, fmt.Errorf("command %d: %w", i, err)
		}
		cn.Commands[i] = cmd
	}

	return cn, nil
}

// canonicalizeAndNode converts an AndNode into canonical form
func canonicalizeAndNode(n *AndNode) (CanonicalNode, error) {
	left, err := toCanonicalNode(n.Left)
	if err != nil {
		return CanonicalNode{}, fmt.Errorf("left: %w", err)
	}

	right, err := toCanonicalNode(n.Right)
	if err != nil {
		return CanonicalNode{}, fmt.Errorf("right: %w", err)
	}

	return CanonicalNode{
		Type:  "and",
		Left:  &left,
		Right: &right,
	}, nil
}

// canonicalizeOrNode converts an OrNode into canonical form
func canonicalizeOrNode(n *OrNode) (CanonicalNode, error) {
	left, err := toCanonicalNode(n.Left)
	if err != nil {
		return CanonicalNode{}, fmt.Errorf("left: %w", err)
	}

	right, err := toCanonicalNode(n.Right)
	if err != nil {
		return CanonicalNode{}, fmt.Errorf("right: %w", err)
	}

	return CanonicalNode{
		Type:  "or",
		Left:  &left,
		Right: &right,
	}, nil
}

// canonicalizeSequenceNode converts a SequenceNode into canonical form
func canonicalizeSequenceNode(n *SequenceNode) (CanonicalNode, error) {
	cn := CanonicalNode{
		Type:  "sequence",
		Nodes: make([]CanonicalNode, len(n.Nodes)),
	}

	for i := range n.Nodes {
		node, err := toCanonicalNode(n.Nodes[i])
		if err != nil {
			return cn, fmt.Errorf("node %d: %w", i, err)
		}
		cn.Nodes[i] = node
	}

	return cn, nil
}

// canonicalizeRedirectNode converts a RedirectNode into canonical form
func canonicalizeRedirectNode(n *RedirectNode) (CanonicalNode, error) {
	source, err := toCanonicalNode(n.Source)
	if err != nil {
		return CanonicalNode{}, fmt.Errorf("source: %w", err)
	}

	target, err := canonicalizeCommandNode(n.Target.Command())
	if err != nil {
		return CanonicalNode{}, fmt.Errorf("target: %w", err)
	}

	return CanonicalNode{
		Type:   "redirect",
		Source: &source,
		Target: &target,
		Mode:   int(n.Mode),
	}, nil
}

func canonicalizeLogicNode(n *LogicNode) (CanonicalNode, error) {
	cn := CanonicalNode{
		Type:      "logic",
		LogicKind: n.Kind,
		Condition: n.Condition,
		Result:    n.Result,
		Block:     make([]CanonicalStep, len(n.Block)),
	}
	for i := range n.Block {
		step, err := canonicalizeStep(&n.Block[i])
		if err != nil {
			return CanonicalNode{}, fmt.Errorf("block step %d: %w", i, err)
		}
		cn.Block[i] = step
	}
	return cn, nil
}

// MarshalBinary produces deterministic CBOR encoding of the canonical plan.
// This ensures byte-for-byte stability across multiple runs.
func (cp *CanonicalPlan) MarshalBinary() ([]byte, error) {
	// Create CBOR encoder with deterministic options
	encMode, err := cbor.CanonicalEncOptions().EncMode()
	if err != nil {
		return nil, fmt.Errorf("failed to create CBOR encoder: %w", err)
	}

	// Create a type alias to avoid infinite recursion
	// (CBOR would call MarshalBinary recursively otherwise)
	type canonicalPlanAlias CanonicalPlan
	alias := (*canonicalPlanAlias)(cp)

	// Encode to CBOR
	data, err := encMode.Marshal(alias)
	if err != nil {
		return nil, fmt.Errorf("CBOR encoding failed: %w", err)
	}

	return data, nil
}

// Hash computes the SHA-256 hash of the canonical plan.
// This is used for plan_hash generation in DisplayIDs.
func (cp *CanonicalPlan) Hash() ([32]byte, error) {
	data, err := cp.MarshalBinary()
	if err != nil {
		return [32]byte{}, err
	}

	return sha256.Sum256(data), nil
}
