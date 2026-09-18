package planfmt

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sort"

	"golang.org/x/crypto/blake2b"

	"github.com/builtwithtofu/sigil/core/invariant"
)

const (
	// Magic is the file magic number "OPAL" (4 bytes)
	Magic = "OPAL"

	// Version is the format version (uint16, little-endian)
	// Version scheme: major.minor encoded as single uint16
	// 0x0001 = version 1.0
	// Breaking changes increment major, additions increment minor
	Version uint16 = 0x0002 // Explicit endpoints and streaming-by-default sink semantics.
)

// Flags is a bitmask for optional features
type Flags uint16

const (
	// FlagCompressed indicates STEPS and VALUES sections are zstd-compressed
	FlagCompressed Flags = 1 << 0

	// FlagSigned indicates a detached Ed25519 signature is present
	FlagSigned Flags = 1 << 1

	// Bits 2-15 reserved for future use
)

// validateUint16 checks if a value fits in uint16, returns error if it exceeds max
func validateUint16(value int, fieldName string) error {
	if value > math.MaxUint16 {
		return fmt.Errorf("%s %d exceeds maximum %d", fieldName, value, math.MaxUint16)
	}
	return nil
}

// Write writes a plan to w and returns the 32-byte file hash (BLAKE2b-256).
// Sorts args and SecretUses before writing to ensure deterministic output.
func Write(w io.Writer, p *Plan) ([32]byte, error) {
	wr := &Writer{w: w}
	return wr.WritePlan(p)
}

// Writer handles writing plans to binary format.
type Writer struct {
	w io.Writer
}

// WritePlan writes the plan to the underlying writer.
// Format: MAGIC(4) | VERSION(2) | FLAGS(2) | HEADER_LEN(4) | BODY_LEN(8) | HEADER | BODY
//
// Returns the BLAKE2b-256 hash of target + body (execution semantics only).
// Metadata (SchemaID, CreatedAt, Compiler) excluded from hash to allow
// timestamp/version updates without invalidating contracts.
func (wr *Writer) WritePlan(p *Plan) ([32]byte, error) {
	// Sort for deterministic encoding (defense in depth - protects against manual Plan construction)
	p.sortArgs()
	p.sortTransports()
	p.sortSecretUses()

	// Buffer first to compute lengths for preamble
	var headerBuf, bodyBuf bytes.Buffer

	if err := wr.writeHeader(&headerBuf, p); err != nil {
		return [32]byte{}, err
	}

	if err := wr.writeBody(&bodyBuf, p); err != nil {
		return [32]byte{}, err
	}

	// Hash target + body only (metadata excluded to allow timestamp/version changes)
	hasher, err := blake2b.New256(nil)
	if err != nil {
		return [32]byte{}, err
	}

	targetBytes := []byte(p.Target)
	if _, err := hasher.Write(targetBytes); err != nil {
		return [32]byte{}, err
	}

	if _, err := hasher.Write(bodyBuf.Bytes()); err != nil {
		return [32]byte{}, err
	}

	var digest [32]byte
	copy(digest[:], hasher.Sum(nil))

	var preambleBuf bytes.Buffer
	if err := wr.writePreambleToBuffer(&preambleBuf, uint32(headerBuf.Len()), uint64(bodyBuf.Len())); err != nil {
		return [32]byte{}, err
	}
	if _, err := wr.w.Write(preambleBuf.Bytes()); err != nil {
		return [32]byte{}, err
	}

	if _, err := wr.w.Write(headerBuf.Bytes()); err != nil {
		return [32]byte{}, err
	}

	if _, err := wr.w.Write(bodyBuf.Bytes()); err != nil {
		return [32]byte{}, err
	}

	return digest, nil
}

// writePreambleToBuffer writes the fixed-size preamble (20 bytes) to a buffer
func (wr *Writer) writePreambleToBuffer(buf *bytes.Buffer, headerLen uint32, bodyLen uint64) error {
	// Magic number (4 bytes)
	if _, err := buf.WriteString(Magic); err != nil {
		return err
	}

	// Version (2 bytes, little-endian)
	if err := binary.Write(buf, binary.LittleEndian, Version); err != nil {
		return err
	}

	flags := Flags(0) // No compression, no signature
	if err := binary.Write(buf, binary.LittleEndian, uint16(flags)); err != nil {
		return err
	}

	if err := binary.Write(buf, binary.LittleEndian, headerLen); err != nil {
		return err
	}

	return binary.Write(buf, binary.LittleEndian, bodyLen)
}

// writeHeader writes the plan header to the buffer
func (wr *Writer) writeHeader(buf *bytes.Buffer, p *Plan) error {
	// Write PlanHeader struct (44 bytes fixed)
	// SchemaID (16 bytes)
	if _, err := buf.Write(p.Header.SchemaID[:]); err != nil {
		return err
	}

	// CreatedAt (8 bytes, uint64, little-endian)
	if err := binary.Write(buf, binary.LittleEndian, p.Header.CreatedAt); err != nil {
		return err
	}

	// Compiler (16 bytes)
	if _, err := buf.Write(p.Header.Compiler[:]); err != nil {
		return err
	}

	// PlanKind (1 byte)
	if err := buf.WriteByte(p.Header.PlanKind); err != nil {
		return err
	}

	// Reserved (3 bytes)
	if _, err := buf.Write([]byte{0, 0, 0}); err != nil {
		return err
	}

	// Target (variable length: 2-byte length prefix + string bytes)
	if err := validateUint16(len(p.Target), "target length"); err != nil {
		return err
	}
	targetLen := uint16(len(p.Target))
	if err := binary.Write(buf, binary.LittleEndian, targetLen); err != nil {
		return err
	}
	if _, err := buf.WriteString(p.Target); err != nil {
		return err
	}

	return nil
}

// writeBody writes the plan body (TOC + sections) to the buffer
func (wr *Writer) writeBody(buf *bytes.Buffer, p *Plan) error {
	// Write step count (2 bytes, uint16)
	if err := validateUint16(len(p.Steps), "step count"); err != nil {
		return err
	}
	stepCount := uint16(len(p.Steps))
	if err := binary.Write(buf, binary.LittleEndian, stepCount); err != nil {
		return err
	}

	// Write each step
	for i := range p.Steps {
		if err := wr.writeStep(buf, &p.Steps[i]); err != nil {
			return err
		}
	}

	// Write transports table
	if err := validateUint16(len(p.Transports), "transport count"); err != nil {
		return err
	}
	transportCount := uint16(len(p.Transports))
	if err := binary.Write(buf, binary.LittleEndian, transportCount); err != nil {
		return err
	}
	for i := range p.Transports {
		if err := wr.writeTransport(buf, &p.Transports[i]); err != nil {
			return err
		}
	}

	// PlanSalt: 32 bytes fixed (zeros if unset for backward compatibility)
	var salt [32]byte
	if len(p.PlanSalt) == 32 {
		copy(salt[:], p.PlanSalt)
	} else if len(p.PlanSalt) != 0 {
		return fmt.Errorf("PlanSalt must be 32 bytes or empty, got %d", len(p.PlanSalt))
	}
	if _, err := buf.Write(salt[:]); err != nil {
		return err
	}

	if err := validateUint16(len(p.SecretUses), "secret uses count"); err != nil {
		return err
	}
	secretUsesCount := uint16(len(p.SecretUses))
	if err := binary.Write(buf, binary.LittleEndian, secretUsesCount); err != nil {
		return err
	}

	for i := range p.SecretUses {
		if err := wr.writeSecretUse(buf, &p.SecretUses[i]); err != nil {
			return err
		}
	}

	return nil
}

// writeSecretUse writes a single SecretUse entry
func (wr *Writer) writeSecretUse(buf *bytes.Buffer, use *SecretUse) error {
	// Write DisplayID (2-byte length + string)
	if err := validateUint16(len(use.DisplayID), "DisplayID length"); err != nil {
		return err
	}
	displayIDLen := uint16(len(use.DisplayID))
	if err := binary.Write(buf, binary.LittleEndian, displayIDLen); err != nil {
		return err
	}
	if _, err := buf.WriteString(use.DisplayID); err != nil {
		return err
	}

	// Write SiteID (2-byte length + string)
	if err := validateUint16(len(use.SiteID), "SiteID length"); err != nil {
		return err
	}
	siteIDLen := uint16(len(use.SiteID))
	if err := binary.Write(buf, binary.LittleEndian, siteIDLen); err != nil {
		return err
	}
	if _, err := buf.WriteString(use.SiteID); err != nil {
		return err
	}

	// Write Site (2-byte length + string)
	if err := validateUint16(len(use.Site), "Site length"); err != nil {
		return err
	}
	siteLen := uint16(len(use.Site))
	if err := binary.Write(buf, binary.LittleEndian, siteLen); err != nil {
		return err
	}
	if _, err := buf.WriteString(use.Site); err != nil {
		return err
	}

	return nil
}

// writeTransport writes a single Transport entry.
func (wr *Writer) writeTransport(buf *bytes.Buffer, transport *Transport) error {
	if err := writeString(buf, transport.ID, "transport ID length"); err != nil {
		return err
	}
	if err := writeString(buf, transport.Decorator, "transport decorator length"); err != nil {
		return err
	}
	if err := writeString(buf, transport.ParentID, "transport parent ID length"); err != nil {
		return err
	}

	if err := validateUint16(len(transport.Args), "transport args count"); err != nil {
		return err
	}
	argsCount := uint16(len(transport.Args))
	if err := binary.Write(buf, binary.LittleEndian, argsCount); err != nil {
		return err
	}
	for i := range transport.Args {
		if err := wr.writeArg(buf, &transport.Args[i]); err != nil {
			return err
		}
	}

	return nil
}

// writeStep writes a single step and its execution tree
func (wr *Writer) writeStep(buf *bytes.Buffer, s *Step) error {
	// INPUT CONTRACT
	invariant.Precondition(s.Tree != nil, "Step.Tree must not be nil (Commands field is ignored)")

	// Write step ID (8 bytes, uint64, little-endian)
	if err := binary.Write(buf, binary.LittleEndian, s.ID); err != nil {
		return err
	}

	// Write execution tree (Commands field is ignored - only exists for executor during transition)
	return wr.writeExecutionNode(buf, s.Tree)
}

// Node type constants for binary serialization
const (
	nodeTypeCommand  = 0x01
	nodeTypePipeline = 0x02
	nodeTypeAnd      = 0x03
	nodeTypeOr       = 0x04
	nodeTypeSequence = 0x05
	nodeTypeTry      = 0x06
	nodeTypeRedirect = 0x07
	nodeTypeLogic    = 0x08
)

// writeExecutionNode writes an execution tree node recursively
func (wr *Writer) writeExecutionNode(buf *bytes.Buffer, node ExecutionNode) error {
	switch n := node.(type) {
	case *CommandNode:
		// Write node type
		if err := buf.WriteByte(nodeTypeCommand); err != nil {
			return err
		}
		// Write command data (reuse writeCommand logic)
		return wr.writeCommand(buf, &CommandNode{
			Decorator:   n.Decorator,
			TransportID: n.TransportID,
			Args:        n.Args,
			Block:       n.Block,
		})

	case *PipelineNode:
		// Write node type
		if err := buf.WriteByte(nodeTypePipeline); err != nil {
			return err
		}
		// Write command count
		if err := validateUint16(len(n.Commands), "pipeline command count"); err != nil {
			return err
		}
		cmdCount := uint16(len(n.Commands))
		if err := binary.Write(buf, binary.LittleEndian, cmdCount); err != nil {
			return err
		}
		// Write each command
		for i := range n.Commands {
			if err := wr.writeExecutionNode(buf, n.Commands[i]); err != nil {
				return err
			}
		}

	case *AndNode:
		// Write node type
		if err := buf.WriteByte(nodeTypeAnd); err != nil {
			return err
		}
		// Write left and right nodes
		if err := wr.writeExecutionNode(buf, n.Left); err != nil {
			return err
		}
		if err := wr.writeExecutionNode(buf, n.Right); err != nil {
			return err
		}

	case *OrNode:
		// Write node type
		if err := buf.WriteByte(nodeTypeOr); err != nil {
			return err
		}
		// Write left and right nodes
		if err := wr.writeExecutionNode(buf, n.Left); err != nil {
			return err
		}
		if err := wr.writeExecutionNode(buf, n.Right); err != nil {
			return err
		}

	case *SequenceNode:
		// Write node type
		if err := buf.WriteByte(nodeTypeSequence); err != nil {
			return err
		}
		// Write node count
		if err := validateUint16(len(n.Nodes), "sequence node count"); err != nil {
			return err
		}
		nodeCount := uint16(len(n.Nodes))
		if err := binary.Write(buf, binary.LittleEndian, nodeCount); err != nil {
			return err
		}
		// Write each node
		for i := range n.Nodes {
			if err := wr.writeExecutionNode(buf, n.Nodes[i]); err != nil {
				return err
			}
		}

	case *TryNode:
		// Write node type
		if err := buf.WriteByte(nodeTypeTry); err != nil {
			return err
		}
		// Write try block step count
		if err := validateUint16(len(n.TryBlock), "try block step count"); err != nil {
			return err
		}
		tryCount := uint16(len(n.TryBlock))
		if err := binary.Write(buf, binary.LittleEndian, tryCount); err != nil {
			return err
		}
		// Write each try block step
		for i := range n.TryBlock {
			if err := wr.writeStep(buf, &n.TryBlock[i]); err != nil {
				return err
			}
		}
		// Write catch block step count
		if err := validateUint16(len(n.CatchBlock), "catch block step count"); err != nil {
			return err
		}
		catchCount := uint16(len(n.CatchBlock))
		if err := binary.Write(buf, binary.LittleEndian, catchCount); err != nil {
			return err
		}
		// Write each catch block step
		for i := range n.CatchBlock {
			if err := wr.writeStep(buf, &n.CatchBlock[i]); err != nil {
				return err
			}
		}
		// Write finally block step count
		if err := validateUint16(len(n.FinallyBlock), "finally block step count"); err != nil {
			return err
		}
		finallyCount := uint16(len(n.FinallyBlock))
		if err := binary.Write(buf, binary.LittleEndian, finallyCount); err != nil {
			return err
		}
		// Write each finally block step
		for i := range n.FinallyBlock {
			if err := wr.writeStep(buf, &n.FinallyBlock[i]); err != nil {
				return err
			}
		}

	case *RedirectNode:
		if err := buf.WriteByte(nodeTypeRedirect); err != nil {
			return err
		}
		if err := wr.writeExecutionNode(buf, n.Source); err != nil {
			return err
		}
		if err := wr.writeCommand(buf, n.Target.Command()); err != nil {
			return err
		}
		return buf.WriteByte(byte(n.Mode))

	case *LogicNode:
		if err := buf.WriteByte(nodeTypeLogic); err != nil {
			return err
		}
		if err := writeString(buf, n.Kind, "logic kind length"); err != nil {
			return err
		}
		if err := writeString(buf, n.Condition, "logic condition length"); err != nil {
			return err
		}
		if err := writeString(buf, n.Result, "logic result length"); err != nil {
			return err
		}
		if err := validateUint16(len(n.Block), "logic block step count"); err != nil {
			return err
		}
		blockCount := uint16(len(n.Block))
		if err := binary.Write(buf, binary.LittleEndian, blockCount); err != nil {
			return err
		}
		for i := range n.Block {
			if err := wr.writeStep(buf, &n.Block[i]); err != nil {
				return err
			}
		}

	default:
		return io.ErrUnexpectedEOF // Unknown node type
	}

	return nil
}

// writeCommand writes a single command
func (wr *Writer) writeCommand(buf *bytes.Buffer, cmd *CommandNode) error {
	// Write decorator (2-byte length + string)
	if err := validateUint16(len(cmd.Decorator), "decorator name length"); err != nil {
		return err
	}
	decoratorLen := uint16(len(cmd.Decorator))
	if err := binary.Write(buf, binary.LittleEndian, decoratorLen); err != nil {
		return err
	}
	if _, err := buf.WriteString(cmd.Decorator); err != nil {
		return err
	}

	// Write transport ID (2-byte length + string)
	if err := validateUint16(len(cmd.TransportID), "transport ID length"); err != nil {
		return err
	}
	transportLen := uint16(len(cmd.TransportID))
	if err := binary.Write(buf, binary.LittleEndian, transportLen); err != nil {
		return err
	}
	if _, err := buf.WriteString(cmd.TransportID); err != nil {
		return err
	}

	// Write args count (2 bytes, uint16)
	if err := validateUint16(len(cmd.Args), "argument count"); err != nil {
		return err
	}
	argsCount := uint16(len(cmd.Args))
	if err := binary.Write(buf, binary.LittleEndian, argsCount); err != nil {
		return err
	}

	// Write each arg
	for i := range cmd.Args {
		if err := wr.writeArg(buf, &cmd.Args[i]); err != nil {
			return err
		}
	}

	// Write block step count (2 bytes, uint16)
	if err := validateUint16(len(cmd.Block), "block step count"); err != nil {
		return err
	}
	blockCount := uint16(len(cmd.Block))
	if err := binary.Write(buf, binary.LittleEndian, blockCount); err != nil {
		return err
	}

	// Write each block step recursively
	for i := range cmd.Block {
		if err := wr.writeStep(buf, &cmd.Block[i]); err != nil {
			return err
		}
	}

	return nil
}

func writeString(buf *bytes.Buffer, value, fieldName string) error {
	if err := validateUint16(len(value), fieldName); err != nil {
		return err
	}
	valueLen := uint16(len(value))
	if err := binary.Write(buf, binary.LittleEndian, valueLen); err != nil {
		return err
	}
	if _, err := buf.WriteString(value); err != nil {
		return err
	}
	return nil
}

// writeArg writes a single argument
func (wr *Writer) writeArg(buf *bytes.Buffer, arg *Arg) error {
	// Write key (2-byte length + string)
	if err := validateUint16(len(arg.Key), "argument key length"); err != nil {
		return err
	}
	keyLen := uint16(len(arg.Key))
	if err := binary.Write(buf, binary.LittleEndian, keyLen); err != nil {
		return err
	}
	if _, err := buf.WriteString(arg.Key); err != nil {
		return err
	}

	return wr.writeValue(buf, &arg.Val)
}

func (wr *Writer) writeValue(buf *bytes.Buffer, val *Value) error {
	// Write value kind (1 byte)
	if err := buf.WriteByte(uint8(val.Kind)); err != nil {
		return err
	}

	return wr.writeValueBody(buf, val)
}

func (wr *Writer) writeValueBody(buf *bytes.Buffer, val *Value) error {
	// Write value based on kind (kind already written)
	switch val.Kind {
	case ValueString:
		// String: 2-byte length + string
		if err := validateUint16(len(val.Str), "string value length"); err != nil {
			return err
		}
		strLen := uint16(len(val.Str))
		if err := binary.Write(buf, binary.LittleEndian, strLen); err != nil {
			return err
		}
		if _, err := buf.WriteString(val.Str); err != nil {
			return err
		}
	case ValueInt:
		// Int: 8 bytes, int64, little-endian
		if err := binary.Write(buf, binary.LittleEndian, val.Int); err != nil {
			return err
		}
	case ValueBool:
		// Bool: 1 byte (0 or 1)
		var b byte
		if val.Bool {
			b = 1
		}
		if err := buf.WriteByte(b); err != nil {
			return err
		}
	case ValuePlaceholder:
		// Placeholder: 4 bytes, uint32 (index into placeholder table)
		if err := binary.Write(buf, binary.LittleEndian, val.Ref); err != nil {
			return err
		}
	case ValueFloat:
		// Float: 8 bytes, float64, little-endian
		if err := binary.Write(buf, binary.LittleEndian, val.Float); err != nil {
			return err
		}
	case ValueDuration:
		// Duration: 2-byte length + string
		if err := validateUint16(len(val.Duration), "duration value length"); err != nil {
			return err
		}
		strLen := uint16(len(val.Duration))
		if err := binary.Write(buf, binary.LittleEndian, strLen); err != nil {
			return err
		}
		if _, err := buf.WriteString(val.Duration); err != nil {
			return err
		}
	case ValueArray:
		if err := validateUint16(len(val.Array), "array length"); err != nil {
			return err
		}
		count := uint16(len(val.Array))
		if err := binary.Write(buf, binary.LittleEndian, count); err != nil {
			return err
		}
		for i := range val.Array {
			if err := wr.writeValue(buf, &val.Array[i]); err != nil {
				return err
			}
		}
	case ValueMap:
		if err := validateUint16(len(val.Map), "map length"); err != nil {
			return err
		}
		count := uint16(len(val.Map))
		if err := binary.Write(buf, binary.LittleEndian, count); err != nil {
			return err
		}
		keys := make([]string, 0, len(val.Map))
		for key := range val.Map {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := writeString(buf, key, "map key"); err != nil {
				return err
			}
			value := val.Map[key]
			if err := wr.writeValue(buf, &value); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unknown value kind: %d", val.Kind)
	}

	return nil
}

// WriteContract writes a contract file with target, hash, and full plan.
//
// Contract format: MAGIC(4) "OPAL" | VERSION(2) 0x0001 | TYPE(1) 'C' | TARGET_LEN(2) | TARGET(var) | HASH(32) | PLAN(binary)
//
// The hash is for fast verification (cryptographic comparison).
// The full plan enables detailed diff display when verification fails, showing users
// exactly what changed (steps added/removed/modified). The plan also enables future
// capabilities like visualization, format conversion, and audit inspection.
func WriteContract(w io.Writer, target string, planHash [32]byte, plan *Plan) error {
	// Create hasher to compute contract hash (not used yet, but for future verification)
	hasher, err := blake2b.New256(nil)
	if err != nil {
		return err
	}
	mw := io.MultiWriter(hasher, w)

	// Write magic "OPAL"
	if _, err := mw.Write([]byte(Magic)); err != nil {
		return err
	}

	// Write version (2 bytes, little-endian)
	if err := binary.Write(mw, binary.LittleEndian, Version); err != nil {
		return err
	}

	// Write type byte 'C' for Contract (distinguishes from full plan)
	if err := binary.Write(mw, binary.LittleEndian, byte('C')); err != nil {
		return err
	}

	// Write target length (2 bytes, little-endian)
	if err := validateUint16(len(target), "target length"); err != nil {
		return err
	}
	targetLen := uint16(len(target))
	if err := binary.Write(mw, binary.LittleEndian, targetLen); err != nil {
		return err
	}

	// Write target string
	if _, err := mw.Write([]byte(target)); err != nil {
		return err
	}

	// Write plan hash (32 bytes)
	if _, err := mw.Write(planHash[:]); err != nil {
		return err
	}

	// Write full binary plan (for diff display when verification fails)
	_, err = Write(w, plan)
	return err
}
