package planfmt

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/builtwithtofu/sigil/core/invariant"
)

// Format limits (enforced by wire types):
// - Op/arg key/value strings: max 65,535 bytes (uint16 length prefix)
// - Args per step: max 65,535 (uint16 count)
// - Children per step: max 65,535 (uint16 count)
// - Recursion depth: max 1,000 levels (enforced by reader)
// - Header size: max 64 KB (enforced by reader)
// - Body size: max 32 MB (enforced by reader)
//
// Version compatibility:
// Readers require an exact uint16 version match. Version 2 binds explicit
// endpoint policies and execution placement; version 1 contracts must be regenerated.

// Plan is the in-memory representation of an execution plan.
// This is the stable contract between planner, executor, and formatters.
//
// # Contract Verification Model
//
// Plans serve as execution contracts that detect environment drift and tampering:
//
// 1. Plan Time: Create plan from source → serialize → hash → store as contract
// 2. Execution Time: Deserialize contract → re-plan with SAME PlanSalt → compare hashes
// 3. If hashes match: Environment unchanged, safe to execute
// 4. If hashes differ: Show diff to user (env vars changed, secrets moved, steps added, etc.)
//
// # Why PlanSalt is Critical
//
// PlanSalt provides both security and deterministic verification:
//
//   - Security: Random salt per contract prevents DisplayID correlation across executions
//     (attacker can't tell if two runs used same secrets by comparing DisplayIDs in logs)
//
//   - Determinism: Contract stores the salt, executor reuses it during re-planning
//     (same source + same salt → same DisplayIDs → same hash → verification works)
//
// Without salt reuse, verification would always fail (random salt → different hash).
//
// # What Contract Verification Detects
//
// - Environment drift: @env.DATABASE_URL changed from "prod" to "staging"
// - Secret authorization changes: API_KEY now authorized at different decorator
// - Source modifications: New steps added, decorators changed
// - Decorator version changes: @retry behavior updated
//
// The full plan is stored in the contract to enable rich diffs showing exactly what changed.
type Plan struct {
	Header     PlanHeader
	Target     string      // Function/command being executed (e.g., "deploy")
	Steps      []Step      // List of steps (newline-separated statements)
	Transports []Transport // Transport table for contract verification
	SecretUses []SecretUse // Authorization list (DisplayID → SiteID mappings)
	PlanSalt   []byte      // Per-plan random salt (32 bytes, for DisplayID derivation)
	Hash       string      // Plan integrity hash (includes SecretUses, computed on Freeze)
	frozen     bool        // Immutability flag (prevents mutations after Freeze)
}

// SecretUse records an authorized use-site for a secret.
// Each SecretUse grants permission for one decorator parameter to unwrap one secret.
// Site-based authority: secrets accessible ONLY at declared sites, no propagation.
type SecretUse struct {
	DisplayID string // Secret identifier (e.g., "sigil:3J98t56A")
	SiteID    string // Canonical site ID (HMAC-based, unforgeable)
	Site      string // Human-readable path (e.g., "root/retry[0]/params/apiKey")
}

// Transport represents a transport context used in the plan.
// Transport IDs are deterministic per plan (seeded by PlanSalt).
type Transport struct {
	ID        string // "transport:abc123"
	Decorator string // "@ssh.connect", "@docker.exec", "local"
	Args      []Arg  // Transport decorator arguments (sorted by Key)
	ParentID  string // Parent transport ID (empty for root/local)
}

// PlanHeader contains metadata about the plan.
// Fields are designed for forward compatibility and versioning.
// Total size: 44 bytes (fixed)
type PlanHeader struct {
	SchemaID  [16]byte // UUID for this format schema version
	CreatedAt uint64   // Unix nanoseconds (UTC)
	Compiler  [16]byte // Build/commit fingerprint
	PlanKind  uint8    // 0=view, 1=contract, 2=executed
	_         [3]byte  // Reserved for future use (align to 8 bytes)
}

// Step represents a single step (newline-separated statement).
// A step can contain multiple commands chained with operators.
//
// IMPORTANT: Always set Tree when creating a Step!
// Binary format only serializes Tree (Commands field is ignored).
//
// Example:
//
//	step := Step{
//	    ID: 1,
//	    Tree: &CommandNode{
//	        Decorator: "@shell",
//	        Args: []Arg{{Key: "command", Val: Value{Kind: ValueString, Str: "echo hello"}}},
//	    },
//	}
//
// Binary Serialization:
// - Writer: Only Tree is serialized (Commands ignored, Tree must not be nil)
// - Reader: Only Tree is deserialized
//
// Invariants:
// - ID must be unique within a plan
// - Tree must not be nil (enforced by writer precondition)
type Step struct {
	ID   uint64        // Unique identifier (stable across plan versions)
	Tree ExecutionNode // Operator precedence tree (REQUIRED - must not be nil)
}

// Arg represents a typed argument to a decorator.
// Args are sorted by Key to ensure deterministic encoding.
type Arg struct {
	Key string
	Val Value
}

// Value is a union type for decorator arguments.
// Only one field should be set based on Kind.
type Value struct {
	Kind ValueKind

	// Union fields (only one valid per Kind)
	Str      string           // For ValueString
	Int      int64            // For ValueInt
	Bool     bool             // For ValueBool
	Ref      uint32           // For ValuePlaceholder (index into placeholder table)
	Float    float64          // For ValueFloat
	Duration string           // For ValueDuration
	Array    []Value          // For ValueArray
	Map      map[string]Value // For ValueMap
}

// ValueKind identifies which field in Value is valid
type ValueKind uint8

const (
	ValueString      ValueKind = iota // Str field valid
	ValueInt                          // Int field valid
	ValueBool                         // Bool field valid
	ValuePlaceholder                  // Ref field valid (placeholder table index)
	ValueFloat                        // Float field valid
	ValueDuration                     // Duration field valid
	ValueArray                        // Array field valid
	ValueMap                          // Map field valid
)

// Validate checks plan invariants
func (p *Plan) Validate() error {
	if len(p.Steps) == 0 {
		return nil // Empty plan is valid
	}

	// Check for duplicate step IDs
	seen := make(map[uint64]bool)
	for i := range p.Steps {
		if err := p.Steps[i].validate(seen); err != nil {
			return err
		}
	}
	return nil
}

// sortArgs sorts decorator arguments by key for deterministic binary encoding.
// Args must be sorted before serialization to ensure byte-for-byte stability.
// Command order is preserved (operator semantics depend on order).
func (p *Plan) sortArgs() {
	for i := range p.Steps {
		p.Steps[i].sortArgs()
	}
}

// sortSecretUses sorts SecretUses by (DisplayID, Site) for deterministic binary encoding.
// Ensures contract hashes are stable regardless of how SecretUses were added to the plan.
func (p *Plan) sortSecretUses() {
	if len(p.SecretUses) > 1 {
		sort.Slice(p.SecretUses, func(i, j int) bool {
			if p.SecretUses[i].DisplayID != p.SecretUses[j].DisplayID {
				return p.SecretUses[i].DisplayID < p.SecretUses[j].DisplayID
			}
			return p.SecretUses[i].Site < p.SecretUses[j].Site
		})
	}
}

// sortTransports sorts Transports by ID for deterministic binary encoding.
func (p *Plan) sortTransports() {
	if len(p.Transports) > 1 {
		sort.Slice(p.Transports, func(i, j int) bool {
			return p.Transports[i].ID < p.Transports[j].ID
		})
	}
	for i := range p.Transports {
		sort.Slice(p.Transports[i].Args, func(a, b int) bool {
			return p.Transports[i].Args[a].Key < p.Transports[i].Args[b].Key
		})
	}
}

// validate checks step invariants recursively
func (s *Step) validate(seen map[uint64]bool) error {
	// Check ID uniqueness
	if seen[s.ID] {
		return fmt.Errorf("duplicate step ID: %d", s.ID)
	}
	seen[s.ID] = true

	// Validate tree (if present)
	if s.Tree != nil {
		return validateNode(s.Tree, s.ID, seen)
	}

	return nil
}

// validateNode recursively validates tree nodes
func validateNode(node ExecutionNode, stepID uint64, seen map[uint64]bool) error {
	switch n := node.(type) {
	case *CommandNode:
		// Check args are sorted
		for j := 1; j < len(n.Args); j++ {
			if n.Args[j-1].Key >= n.Args[j].Key {
				return fmt.Errorf("step %d: args not sorted (key %q >= %q)",
					stepID, n.Args[j-1].Key, n.Args[j].Key)
			}
		}
		// Validate block steps recursively
		for j := range n.Block {
			if err := n.Block[j].validate(seen); err != nil {
				return err
			}
		}

	case *PipelineNode:
		for i := range n.Commands {
			if err := validateNode(n.Commands[i], stepID, seen); err != nil {
				return err
			}
		}

	case *AndNode:
		if err := validateNode(n.Left, stepID, seen); err != nil {
			return err
		}
		if err := validateNode(n.Right, stepID, seen); err != nil {
			return err
		}

	case *OrNode:
		if err := validateNode(n.Left, stepID, seen); err != nil {
			return err
		}
		if err := validateNode(n.Right, stepID, seen); err != nil {
			return err
		}

	case *SequenceNode:
		for i := range n.Nodes {
			if err := validateNode(n.Nodes[i], stepID, seen); err != nil {
				return err
			}
		}

	case *RedirectNode:
		if err := validateNode(n.Source, stepID, seen); err != nil {
			return err
		}
		if err := validateNode(n.Target.Command(), stepID, seen); err != nil {
			return err
		}

	case *LogicNode:
		for i := range n.Block {
			if err := n.Block[i].validate(seen); err != nil {
				return err
			}
		}

	case *TryNode:
		for i := range n.TryBlock {
			if err := n.TryBlock[i].validate(seen); err != nil {
				return err
			}
		}
		for i := range n.CatchBlock {
			if err := n.CatchBlock[i].validate(seen); err != nil {
				return err
			}
		}
		for i := range n.FinallyBlock {
			if err := n.FinallyBlock[i].validate(seen); err != nil {
				return err
			}
		}
	}

	return nil
}

// sortArgs sorts args in the execution tree
func (s *Step) sortArgs() {
	if s.Tree != nil {
		sortArgsInNode(s.Tree)
	}
}

// sortArgsInNode recursively sorts args in all tree nodes
func sortArgsInNode(node ExecutionNode) {
	switch n := node.(type) {
	case *CommandNode:
		// Sort args by key for deterministic encoding
		if len(n.Args) > 1 {
			sort.Slice(n.Args, func(i, j int) bool {
				return n.Args[i].Key < n.Args[j].Key
			})
		}
		// Recurse into block steps
		for i := range n.Block {
			n.Block[i].sortArgs()
		}

	case *PipelineNode:
		for i := range n.Commands {
			sortArgsInNode(n.Commands[i])
		}

	case *AndNode:
		sortArgsInNode(n.Left)
		sortArgsInNode(n.Right)

	case *OrNode:
		sortArgsInNode(n.Left)
		sortArgsInNode(n.Right)

	case *SequenceNode:
		for i := range n.Nodes {
			sortArgsInNode(n.Nodes[i])
		}

	case *RedirectNode:
		sortArgsInNode(n.Source)
		sortArgsInNode(n.Target.Command())

	case *LogicNode:
		for i := range n.Block {
			n.Block[i].sortArgs()
		}

	case *TryNode:
		for i := range n.TryBlock {
			n.TryBlock[i].sortArgs()
		}
		for i := range n.CatchBlock {
			n.CatchBlock[i].sortArgs()
		}
		for i := range n.FinallyBlock {
			n.FinallyBlock[i].sortArgs()
		}
	}
}

// Digest computes BLAKE2b-256 hash of the complete serialized plan.
// Includes DisplayIDs (used for contract verification after DisplayIDs are generated).
// For structure-only hashing before DisplayIDs exist, use Canonicalize().Hash() instead.
// Returns hex-encoded hash: "blake2b:a3f8b2c1d4e5f6a7..."
func (p *Plan) Digest() (string, error) {
	var buf bytes.Buffer
	hash, err := Write(&buf, p)
	if err != nil {
		return "", fmt.Errorf("failed to serialize plan for digest: %w", err)
	}

	return fmt.Sprintf("blake2b:%x", hash), nil
}

// NewPlan creates a new Plan with a random PlanSalt.
// The salt is used for deterministic DisplayID generation within this plan.
func NewPlan() *Plan {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		panic(fmt.Sprintf("failed to generate plan salt: %v", err))
	}
	return &Plan{
		PlanSalt: salt,
	}
}

// AddSecretUse adds a SecretUse to the plan's authorization list.
// Returns error if plan is frozen (immutable after Freeze()).
func (p *Plan) AddSecretUse(use SecretUse) error {
	if p.frozen {
		return fmt.Errorf("cannot modify frozen plan")
	}
	p.SecretUses = append(p.SecretUses, use)
	return nil
}

// Freeze computes the plan hash and marks the plan as immutable.
// After freezing, AddSecretUse will return an error.
// The hash includes all security-relevant data to prevent tampering.
func (p *Plan) Freeze() {
	p.Hash = p.ComputeHash()
	p.frozen = true
}

// ComputeHash computes BLAKE2b-256 hash of the complete serialized plan.
// Uses binary serialization (via Write) for deterministic encoding across Go versions.
// Includes all security-relevant fields: Target, Steps, Secrets, SecretUses, PlanSalt, Header.
func (p *Plan) ComputeHash() string {
	var buf bytes.Buffer
	hash, err := Write(&buf, p)
	invariant.ExpectNoError(err, "plan serialization failed (bytes.Buffer never fails)")

	// Write returns BLAKE2b-256 hash as [32]byte, convert to hex string
	return hex.EncodeToString(hash[:])
}
