package planfmt

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"golang.org/x/crypto/blake2b"
)

// Read reads a plan from r and returns the plan and its hash.
func Read(r io.Reader) (*Plan, [32]byte, error) {
	rd := &Reader{r: r}
	return rd.ReadPlan()
}

// Reader handles reading plans from binary format.
type Reader struct {
	r io.Reader
}

// ReadPlan reads the plan from the underlying reader and returns the computed hash.
func (rd *Reader) ReadPlan() (*Plan, [32]byte, error) {
	// Create hasher to compute hash while reading
	hasher, err := blake2b.New256(nil)
	if err != nil {
		return nil, [32]byte{}, fmt.Errorf("create hasher: %w", err)
	}

	// Read preamble (20 bytes) - not included in hash (metadata)
	var preamble [20]byte
	if _, err := io.ReadFull(rd.r, preamble[:]); err != nil {
		return nil, [32]byte{}, fmt.Errorf("read preamble: %w", err)
	}

	// Verify magic
	magic := string(preamble[0:4])
	if magic != Magic {
		return nil, [32]byte{}, fmt.Errorf("invalid magic: got %q, expected %q", magic, Magic)
	}

	// Read version
	version := binary.LittleEndian.Uint16(preamble[4:6])
	if version != Version {
		return nil, [32]byte{}, fmt.Errorf("unsupported version: got 0x%04x, expected 0x%04x", version, Version)
	}

	// Read flags
	flags := Flags(binary.LittleEndian.Uint16(preamble[6:8]))

	// Reject unknown flags for this version
	knownFlags := FlagCompressed | FlagSigned
	if flags&^knownFlags != 0 {
		return nil, [32]byte{}, fmt.Errorf("unsupported flags: 0x%04x (unknown bits: 0x%04x)", flags, flags&^knownFlags)
	}

	// TODO: Implement compression and signature verification (post-v1.0)
	if flags&FlagCompressed != 0 {
		return nil, [32]byte{}, fmt.Errorf("compressed plans not yet supported")
	}
	if flags&FlagSigned != 0 {
		return nil, [32]byte{}, fmt.Errorf("signed plans not yet supported")
	}

	// Read header length
	headerLen := binary.LittleEndian.Uint32(preamble[8:12])

	// Read body length
	bodyLen := binary.LittleEndian.Uint64(preamble[12:20])

	// Validate lengths to prevent OOM attacks
	// Header: metadata only, should be < 1KB typically
	// Body: even 10,000 steps should fit in ~10MB
	const maxHeaderLen = 64 * 1024      // 64KB max header (very generous)
	const maxBodyLen = 32 * 1024 * 1024 // 32MB max body (lowered for fuzz safety)
	const maxDepth = 1000               // Max recursion depth to prevent stack overflow

	if headerLen > maxHeaderLen {
		return nil, [32]byte{}, fmt.Errorf("header length %d exceeds maximum %d", headerLen, maxHeaderLen)
	}
	if bodyLen > maxBodyLen {
		return nil, [32]byte{}, fmt.Errorf("body length %d exceeds maximum %d", bodyLen, maxBodyLen)
	}

	// Read header (metadata - not included in hash except target)
	headerBuf := make([]byte, headerLen)
	if _, err := io.ReadFull(rd.r, headerBuf); err != nil {
		return nil, [32]byte{}, fmt.Errorf("read header: %w", err)
	}

	plan, err := rd.readHeader(bytes.NewReader(headerBuf))
	if err != nil {
		return nil, [32]byte{}, fmt.Errorf("parse header: %w", err)
	}

	// Read body (execution semantics - included in hash)
	bodyBuf := make([]byte, bodyLen)
	if _, err := io.ReadFull(rd.r, bodyBuf); err != nil {
		return nil, [32]byte{}, fmt.Errorf("read body: %w", err)
	}

	if err := rd.readBody(bytes.NewReader(bodyBuf), plan, maxDepth); err != nil {
		return nil, [32]byte{}, fmt.Errorf("parse body: %w", err)
	}

	// Compute hash of target + body (execution semantics only)
	// Target is part of execution semantics (which function to run)
	// Metadata (SchemaID, CreatedAt, Compiler) is excluded from hash
	if _, err := hasher.Write([]byte(plan.Target)); err != nil {
		return nil, [32]byte{}, fmt.Errorf("hash target: %w", err)
	}
	if _, err := hasher.Write(bodyBuf); err != nil {
		return nil, [32]byte{}, fmt.Errorf("hash body: %w", err)
	}

	// Extract hash
	var digest [32]byte
	copy(digest[:], hasher.Sum(nil))
	return plan, digest, nil
}

// readHeader reads the plan header
func (rd *Reader) readHeader(r io.Reader) (*Plan, error) {
	plan := &Plan{}

	// Read SchemaID (16 bytes)
	if _, err := io.ReadFull(r, plan.Header.SchemaID[:]); err != nil {
		return nil, fmt.Errorf("read schema ID: %w", err)
	}

	// Read CreatedAt (8 bytes, uint64, little-endian)
	if err := binary.Read(r, binary.LittleEndian, &plan.Header.CreatedAt); err != nil {
		return nil, fmt.Errorf("read created at: %w", err)
	}

	// Read Compiler (16 bytes)
	if _, err := io.ReadFull(r, plan.Header.Compiler[:]); err != nil {
		return nil, fmt.Errorf("read compiler: %w", err)
	}

	// Read PlanKind (1 byte)
	var planKind byte
	if err := binary.Read(r, binary.LittleEndian, &planKind); err != nil {
		return nil, fmt.Errorf("read plan kind: %w", err)
	}
	plan.Header.PlanKind = planKind

	// Skip reserved (3 bytes)
	var reserved [3]byte
	if _, err := io.ReadFull(r, reserved[:]); err != nil {
		return nil, fmt.Errorf("read reserved: %w", err)
	}

	// Read Target length (2 bytes, uint16, little-endian)
	var targetLen uint16
	if err := binary.Read(r, binary.LittleEndian, &targetLen); err != nil {
		return nil, fmt.Errorf("read target length: %w", err)
	}

	// Read Target string
	targetBuf := make([]byte, targetLen)
	if _, err := io.ReadFull(r, targetBuf); err != nil {
		return nil, fmt.Errorf("read target: %w", err)
	}
	plan.Target = string(targetBuf)

	return plan, nil
}

// readBody reads the plan body (steps)
func (rd *Reader) readBody(r io.Reader, plan *Plan, maxDepth int) error {
	// Read step count (2 bytes, uint16, little-endian)
	var stepCount uint16
	if err := binary.Read(r, binary.LittleEndian, &stepCount); err != nil {
		if err == io.EOF {
			// Empty body, no steps
			return nil
		}
		return fmt.Errorf("read step count: %w", err)
	}

	// Read each step
	if stepCount > 0 {
		plan.Steps = make([]Step, stepCount)
		for i := 0; i < int(stepCount); i++ {
			step, err := rd.readStep(r, 0, maxDepth)
			if err != nil {
				return fmt.Errorf("read step %d: %w", i, err)
			}
			plan.Steps[i] = *step
		}
	}

	// Read transport count (2 bytes, uint16)
	var transportCount uint16
	if err := binary.Read(r, binary.LittleEndian, &transportCount); err != nil {
		return fmt.Errorf("read transport count: %w", err)
	}

	// Read each transport
	if transportCount > 0 {
		plan.Transports = make([]Transport, transportCount)
		for i := 0; i < int(transportCount); i++ {
			transport, err := rd.readTransport(r)
			if err != nil {
				return fmt.Errorf("read transport %d: %w", i, err)
			}
			plan.Transports[i] = *transport
		}
	}

	// Read PlanSalt (32 bytes, fixed size)
	saltBytes := make([]byte, 32)
	if _, err := io.ReadFull(r, saltBytes); err != nil {
		return fmt.Errorf("read plan salt: %w", err)
	}
	// Only set PlanSalt if non-zero (backward compatibility)
	isZero := true
	for _, b := range saltBytes {
		if b != 0 {
			isZero = false
			break
		}
	}
	if !isZero {
		plan.PlanSalt = saltBytes
	}

	// Read SecretUses count (2 bytes, uint16)
	var secretUsesCount uint16
	if err := binary.Read(r, binary.LittleEndian, &secretUsesCount); err != nil {
		return fmt.Errorf("read secret uses count: %w", err)
	}

	// Read each SecretUse
	if secretUsesCount > 0 {
		plan.SecretUses = make([]SecretUse, secretUsesCount)
		for i := 0; i < int(secretUsesCount); i++ {
			use, err := rd.readSecretUse(r)
			if err != nil {
				return fmt.Errorf("read secret use %d: %w", i, err)
			}
			plan.SecretUses[i] = *use
		}
	}

	return nil
}

// readSecretUse reads a single SecretUse entry
func (rd *Reader) readSecretUse(r io.Reader) (*SecretUse, error) {
	use := &SecretUse{}

	// Read DisplayID (2-byte length + string)
	var displayIDLen uint16
	if err := binary.Read(r, binary.LittleEndian, &displayIDLen); err != nil {
		return nil, fmt.Errorf("read DisplayID length: %w", err)
	}
	displayIDBytes := make([]byte, displayIDLen)
	if _, err := io.ReadFull(r, displayIDBytes); err != nil {
		return nil, fmt.Errorf("read DisplayID: %w", err)
	}
	use.DisplayID = string(displayIDBytes)

	// Read SiteID (2-byte length + string)
	var siteIDLen uint16
	if err := binary.Read(r, binary.LittleEndian, &siteIDLen); err != nil {
		return nil, fmt.Errorf("read SiteID length: %w", err)
	}
	siteIDBytes := make([]byte, siteIDLen)
	if _, err := io.ReadFull(r, siteIDBytes); err != nil {
		return nil, fmt.Errorf("read SiteID: %w", err)
	}
	use.SiteID = string(siteIDBytes)

	// Read Site (2-byte length + string)
	var siteLen uint16
	if err := binary.Read(r, binary.LittleEndian, &siteLen); err != nil {
		return nil, fmt.Errorf("read Site length: %w", err)
	}
	siteBytes := make([]byte, siteLen)
	if _, err := io.ReadFull(r, siteBytes); err != nil {
		return nil, fmt.Errorf("read Site: %w", err)
	}
	use.Site = string(siteBytes)

	return use, nil
}

// readTransport reads a single Transport entry.
func (rd *Reader) readTransport(r io.Reader) (*Transport, error) {
	transport := &Transport{}

	var err error
	transport.ID, err = readString(r, "transport ID")
	if err != nil {
		return nil, err
	}
	transport.Decorator, err = readString(r, "transport decorator")
	if err != nil {
		return nil, err
	}
	transport.ParentID, err = readString(r, "transport parent ID")
	if err != nil {
		return nil, err
	}

	var argsCount uint16
	if err := binary.Read(r, binary.LittleEndian, &argsCount); err != nil {
		return nil, fmt.Errorf("read transport args count: %w", err)
	}
	if argsCount > 0 {
		transport.Args = make([]Arg, argsCount)
		for i := 0; i < int(argsCount); i++ {
			arg, err := rd.readArg(r, 0, 1000)
			if err != nil {
				return nil, fmt.Errorf("read transport arg %d: %w", i, err)
			}
			transport.Args[i] = *arg
		}
	}

	return transport, nil
}

// readStep reads a single step and its commands recursively
func (rd *Reader) readStep(r io.Reader, depth, maxDepth int) (*Step, error) {
	// Check depth limit to prevent stack overflow
	if depth >= maxDepth {
		return nil, fmt.Errorf("max recursion depth %d exceeded", maxDepth)
	}

	step := &Step{}

	// Read step ID (8 bytes, uint64, little-endian)
	if err := binary.Read(r, binary.LittleEndian, &step.ID); err != nil {
		return nil, fmt.Errorf("read step ID: %w", err)
	}

	// Read execution tree (Commands field not serialized - only exists in-memory for executor)
	node, err := rd.readExecutionNode(r, depth, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("read execution tree: %w", err)
	}
	step.Tree = node

	return step, nil
}

// readExecutionNode reads an execution tree node recursively
func (rd *Reader) readExecutionNode(r io.Reader, depth, maxDepth int) (ExecutionNode, error) {
	// Check depth limit
	if depth >= maxDepth {
		return nil, fmt.Errorf("max recursion depth %d exceeded", maxDepth)
	}

	// Read node type (1 byte)
	var nodeType byte
	if err := binary.Read(r, binary.LittleEndian, &nodeType); err != nil {
		return nil, fmt.Errorf("read node type: %w", err)
	}

	switch nodeType {
	case 0x01: // CommandNode
		// Read command data
		cmd, err := rd.readCommand(r, depth+1, maxDepth)
		if err != nil {
			return nil, fmt.Errorf("read command node: %w", err)
		}
		return &CommandNode{
			Decorator:   cmd.Decorator,
			TransportID: cmd.TransportID,
			Args:        cmd.Args,
			Block:       cmd.Block,
		}, nil

	case 0x02: // PipelineNode
		// Read command count
		var cmdCount uint16
		if err := binary.Read(r, binary.LittleEndian, &cmdCount); err != nil {
			return nil, fmt.Errorf("read pipeline command count: %w", err)
		}
		// Read commands (can be CommandNode or RedirectNode)
		commands := make([]ExecutionNode, cmdCount)
		for i := 0; i < int(cmdCount); i++ {
			node, err := rd.readExecutionNode(r, depth+1, maxDepth)
			if err != nil {
				return nil, fmt.Errorf("read pipeline command %d: %w", i, err)
			}
			// Validate that pipeline elements are CommandNode or RedirectNode
			switch node.(type) {
			case *CommandNode, *RedirectNode:
				commands[i] = node
			default:
				return nil, fmt.Errorf("pipeline must contain CommandNode or RedirectNode, got %T", node)
			}
		}
		return &PipelineNode{Commands: commands}, nil

	case 0x03: // AndNode
		// Read left node
		left, err := rd.readExecutionNode(r, depth+1, maxDepth)
		if err != nil {
			return nil, fmt.Errorf("read AND left: %w", err)
		}
		// Read right node
		right, err := rd.readExecutionNode(r, depth+1, maxDepth)
		if err != nil {
			return nil, fmt.Errorf("read AND right: %w", err)
		}
		return &AndNode{Left: left, Right: right}, nil

	case 0x04: // OrNode
		// Read left node
		left, err := rd.readExecutionNode(r, depth+1, maxDepth)
		if err != nil {
			return nil, fmt.Errorf("read OR left: %w", err)
		}
		// Read right node
		right, err := rd.readExecutionNode(r, depth+1, maxDepth)
		if err != nil {
			return nil, fmt.Errorf("read OR right: %w", err)
		}
		return &OrNode{Left: left, Right: right}, nil

	case 0x05: // SequenceNode
		// Read node count
		var nodeCount uint16
		if err := binary.Read(r, binary.LittleEndian, &nodeCount); err != nil {
			return nil, fmt.Errorf("read sequence node count: %w", err)
		}
		// Read nodes
		nodes := make([]ExecutionNode, nodeCount)
		for i := 0; i < int(nodeCount); i++ {
			node, err := rd.readExecutionNode(r, depth+1, maxDepth)
			if err != nil {
				return nil, fmt.Errorf("read sequence node %d: %w", i, err)
			}
			nodes[i] = node
		}
		return &SequenceNode{Nodes: nodes}, nil

	case 0x06: // TryNode
		// Read try block step count
		var tryCount uint16
		if err := binary.Read(r, binary.LittleEndian, &tryCount); err != nil {
			return nil, fmt.Errorf("read try block step count: %w", err)
		}
		// Read try block steps
		tryBlock := make([]Step, tryCount)
		for i := 0; i < int(tryCount); i++ {
			step, err := rd.readStep(r, depth+1, maxDepth)
			if err != nil {
				return nil, fmt.Errorf("read try block step %d: %w", i, err)
			}
			tryBlock[i] = *step
		}
		// Read catch block step count
		var catchCount uint16
		if err := binary.Read(r, binary.LittleEndian, &catchCount); err != nil {
			return nil, fmt.Errorf("read catch block step count: %w", err)
		}
		// Read catch block steps
		catchBlock := make([]Step, catchCount)
		for i := 0; i < int(catchCount); i++ {
			step, err := rd.readStep(r, depth+1, maxDepth)
			if err != nil {
				return nil, fmt.Errorf("read catch block step %d: %w", i, err)
			}
			catchBlock[i] = *step
		}
		// Read finally block step count
		var finallyCount uint16
		if err := binary.Read(r, binary.LittleEndian, &finallyCount); err != nil {
			return nil, fmt.Errorf("read finally block step count: %w", err)
		}
		// Read finally block steps
		finallyBlock := make([]Step, finallyCount)
		for i := 0; i < int(finallyCount); i++ {
			step, err := rd.readStep(r, depth+1, maxDepth)
			if err != nil {
				return nil, fmt.Errorf("read finally block step %d: %w", i, err)
			}
			finallyBlock[i] = *step
		}
		return &TryNode{
			TryBlock:     tryBlock,
			CatchBlock:   catchBlock,
			FinallyBlock: finallyBlock,
		}, nil

	case 0x07: // RedirectNode
		source, err := rd.readExecutionNode(r, depth+1, maxDepth)
		if err != nil {
			return nil, fmt.Errorf("read redirect source: %w", err)
		}
		target, err := rd.readCommand(r, depth+1, maxDepth)
		if err != nil {
			return nil, fmt.Errorf("read redirect target: %w", err)
		}
		var modeByte byte
		if err := binary.Read(r, binary.LittleEndian, &modeByte); err != nil {
			return nil, fmt.Errorf("read redirect mode: %w", err)
		}
		return &RedirectNode{
			Source: source,
			Target: EndpointSpec{Decorator: target.Decorator, TransportID: target.TransportID, Args: target.Args},
			Mode:   RedirectMode(modeByte),
		}, nil

	case 0x08: // LogicNode
		kind, err := readString(r, "logic kind")
		if err != nil {
			return nil, err
		}
		condition, err := readString(r, "logic condition")
		if err != nil {
			return nil, err
		}
		result, err := readString(r, "logic result")
		if err != nil {
			return nil, err
		}
		var blockCount uint16
		if err := binary.Read(r, binary.LittleEndian, &blockCount); err != nil {
			return nil, fmt.Errorf("read logic block count: %w", err)
		}
		block := make([]Step, blockCount)
		for i := 0; i < int(blockCount); i++ {
			step, err := rd.readStep(r, depth+1, maxDepth)
			if err != nil {
				return nil, fmt.Errorf("read logic block step %d: %w", i, err)
			}
			block[i] = *step
		}
		return &LogicNode{
			Kind:      kind,
			Condition: condition,
			Result:    result,
			Block:     block,
		}, nil

	default:
		return nil, fmt.Errorf("unknown node type: 0x%02x", nodeType)
	}
}

func readString(r io.Reader, fieldName string) (string, error) {
	var valueLen uint16
	if err := binary.Read(r, binary.LittleEndian, &valueLen); err != nil {
		return "", fmt.Errorf("read %s length: %w", fieldName, err)
	}
	valueBytes := make([]byte, valueLen)
	if _, err := io.ReadFull(r, valueBytes); err != nil {
		return "", fmt.Errorf("read %s: %w", fieldName, err)
	}
	return string(valueBytes), nil
}

// readCommand reads a single command
func (rd *Reader) readCommand(r io.Reader, depth, maxDepth int) (*CommandNode, error) {
	cmd := &CommandNode{}

	// Read decorator length (2 bytes, uint16, little-endian)
	var decoratorLen uint16
	if err := binary.Read(r, binary.LittleEndian, &decoratorLen); err != nil {
		return nil, fmt.Errorf("read decorator length: %w", err)
	}

	// Read decorator string
	decoratorBuf := make([]byte, decoratorLen)
	if _, err := io.ReadFull(r, decoratorBuf); err != nil {
		return nil, fmt.Errorf("read decorator: %w", err)
	}
	cmd.Decorator = string(decoratorBuf)

	// Read transport ID length (2 bytes, uint16, little-endian)
	var transportLen uint16
	if err := binary.Read(r, binary.LittleEndian, &transportLen); err != nil {
		return nil, fmt.Errorf("read transport ID length: %w", err)
	}

	// Read transport ID string
	if transportLen > 0 {
		transportBuf := make([]byte, transportLen)
		if _, err := io.ReadFull(r, transportBuf); err != nil {
			return nil, fmt.Errorf("read transport ID: %w", err)
		}
		cmd.TransportID = string(transportBuf)
	}

	// Read args count (2 bytes, uint16, little-endian)
	var argsCount uint16
	if err := binary.Read(r, binary.LittleEndian, &argsCount); err != nil {
		return nil, fmt.Errorf("read args count: %w", err)
	}

	// Read each arg
	if argsCount > 0 {
		cmd.Args = make([]Arg, argsCount)
		for i := 0; i < int(argsCount); i++ {
			arg, err := rd.readArg(r, depth, maxDepth)
			if err != nil {
				return nil, fmt.Errorf("read arg %d: %w", i, err)
			}
			cmd.Args[i] = *arg
		}
	}

	// Read block step count (2 bytes, uint16, little-endian)
	var blockCount uint16
	if err := binary.Read(r, binary.LittleEndian, &blockCount); err != nil {
		return nil, fmt.Errorf("read block count: %w", err)
	}

	// Read each block step recursively
	if blockCount > 0 {
		cmd.Block = make([]Step, blockCount)
		for i := 0; i < int(blockCount); i++ {
			step, err := rd.readStep(r, depth+1, maxDepth)
			if err != nil {
				return nil, fmt.Errorf("read block step %d: %w", i, err)
			}
			cmd.Block[i] = *step
		}
	}

	return cmd, nil
}

// readArg reads a single argument
func (rd *Reader) readArg(r io.Reader, depth, maxDepth int) (*Arg, error) {
	arg := &Arg{}

	// Read key length (2 bytes, uint16, little-endian)
	var keyLen uint16
	if err := binary.Read(r, binary.LittleEndian, &keyLen); err != nil {
		return nil, fmt.Errorf("read key length: %w", err)
	}

	// Read key string
	keyBuf := make([]byte, keyLen)
	if _, err := io.ReadFull(r, keyBuf); err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}
	arg.Key = string(keyBuf)

	// Read value kind (1 byte)
	var kind byte
	if err := binary.Read(r, binary.LittleEndian, &kind); err != nil {
		return nil, fmt.Errorf("read value kind: %w", err)
	}
	arg.Val.Kind = ValueKind(kind)

	if err := rd.readValueBody(r, &arg.Val, depth, maxDepth); err != nil {
		return nil, err
	}

	return arg, nil
}

func (rd *Reader) readValue(r io.Reader, depth, maxDepth int) (Value, error) {
	// Check depth limit to prevent stack overflow
	if depth >= maxDepth {
		return Value{}, fmt.Errorf("max value recursion depth %d exceeded", maxDepth)
	}

	var kind byte
	if err := binary.Read(r, binary.LittleEndian, &kind); err != nil {
		return Value{}, fmt.Errorf("read value kind: %w", err)
	}
	val := Value{Kind: ValueKind(kind)}
	if err := rd.readValueBody(r, &val, depth, maxDepth); err != nil {
		return Value{}, err
	}
	return val, nil
}

func (rd *Reader) readValueBody(r io.Reader, val *Value, depth, maxDepth int) error {
	switch val.Kind {
	case ValueString:
		// String: 2-byte length + string
		var strLen uint16
		if err := binary.Read(r, binary.LittleEndian, &strLen); err != nil {
			return fmt.Errorf("read string length: %w", err)
		}
		strBuf := make([]byte, strLen)
		if _, err := io.ReadFull(r, strBuf); err != nil {
			return fmt.Errorf("read string: %w", err)
		}
		val.Str = string(strBuf)

	case ValueInt:
		// Int: 8 bytes, int64, little-endian
		if err := binary.Read(r, binary.LittleEndian, &val.Int); err != nil {
			return fmt.Errorf("read int: %w", err)
		}

	case ValueBool:
		// Bool: 1 byte (0 or 1)
		var b byte
		if err := binary.Read(r, binary.LittleEndian, &b); err != nil {
			return fmt.Errorf("read bool: %w", err)
		}
		val.Bool = b != 0

	case ValuePlaceholder:
		// Placeholder: 4 bytes, uint32 (index into placeholder table)
		if err := binary.Read(r, binary.LittleEndian, &val.Ref); err != nil {
			return fmt.Errorf("read placeholder ref: %w", err)
		}

	case ValueFloat:
		// Float: 8 bytes, float64, little-endian
		if err := binary.Read(r, binary.LittleEndian, &val.Float); err != nil {
			return fmt.Errorf("read float: %w", err)
		}

	case ValueDuration:
		// Duration: 2-byte length + string
		var strLen uint16
		if err := binary.Read(r, binary.LittleEndian, &strLen); err != nil {
			return fmt.Errorf("read duration length: %w", err)
		}
		strBuf := make([]byte, strLen)
		if _, err := io.ReadFull(r, strBuf); err != nil {
			return fmt.Errorf("read duration: %w", err)
		}
		val.Duration = string(strBuf)

	case ValueArray:
		var count uint16
		if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
			return fmt.Errorf("read array length: %w", err)
		}
		val.Array = make([]Value, count)
		for i := 0; i < int(count); i++ {
			item, err := rd.readValue(r, depth+1, maxDepth)
			if err != nil {
				return err
			}
			val.Array[i] = item
		}

	case ValueMap:
		var count uint16
		if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
			return fmt.Errorf("read map length: %w", err)
		}
		val.Map = make(map[string]Value, count)
		for i := 0; i < int(count); i++ {
			var keyLen uint16
			if err := binary.Read(r, binary.LittleEndian, &keyLen); err != nil {
				return fmt.Errorf("read map key length: %w", err)
			}
			keyBuf := make([]byte, keyLen)
			if _, err := io.ReadFull(r, keyBuf); err != nil {
				return fmt.Errorf("read map key: %w", err)
			}
			key := string(keyBuf)
			value, err := rd.readValue(r, depth+1, maxDepth)
			if err != nil {
				return err
			}
			val.Map[key] = value
		}

	default:
		return fmt.Errorf("unknown value kind: %d", val.Kind)
	}

	return nil
}

// ReadContract reads a contract file and returns target, hash, and full plan.
//
// Contract format: MAGIC(4) "OPAL" | VERSION(2) 0x0001 | TYPE(1) 'C' | TARGET_LEN(2) | TARGET(var) | HASH(32) | PLAN(binary)
//
// The hash is used for verification (compare against fresh plan hash).
// The plan is used for diff display when verification fails, enabling detailed
// comparison to show users exactly what changed.
func ReadContract(r io.Reader) (target string, planHash [32]byte, plan *Plan, err error) {
	// Read and verify magic
	magic := make([]byte, 4)
	if _, err := io.ReadFull(r, magic); err != nil {
		return "", [32]byte{}, nil, fmt.Errorf("failed to read magic: %w", err)
	}
	if string(magic) != Magic {
		return "", [32]byte{}, nil, fmt.Errorf("invalid magic: expected %q, got %q", Magic, string(magic))
	}

	// Read and verify version
	var version uint16
	if err := binary.Read(r, binary.LittleEndian, &version); err != nil {
		return "", [32]byte{}, nil, fmt.Errorf("failed to read version: %w", err)
	}
	if version != Version {
		return "", [32]byte{}, nil, fmt.Errorf("unsupported version: %d (expected %d)", version, Version)
	}

	// Read and verify type byte
	var typeByte byte
	if err := binary.Read(r, binary.LittleEndian, &typeByte); err != nil {
		return "", [32]byte{}, nil, fmt.Errorf("failed to read type: %w", err)
	}
	if typeByte != 'C' {
		return "", [32]byte{}, nil, fmt.Errorf("not a contract file: type byte is %q (expected 'C')", typeByte)
	}

	// Read target length
	var targetLen uint16
	if err := binary.Read(r, binary.LittleEndian, &targetLen); err != nil {
		return "", [32]byte{}, nil, fmt.Errorf("failed to read target length: %w", err)
	}

	// Read target string
	targetBytes := make([]byte, targetLen)
	if _, err := io.ReadFull(r, targetBytes); err != nil {
		return "", [32]byte{}, nil, fmt.Errorf("failed to read target: %w", err)
	}
	target = string(targetBytes)

	// Read plan hash (32 bytes)
	if _, err := io.ReadFull(r, planHash[:]); err != nil {
		return "", [32]byte{}, nil, fmt.Errorf("failed to read plan hash: %w", err)
	}

	// Read full binary plan (for diff display when verification fails)
	plan, _, err = Read(r)
	if err != nil {
		return "", [32]byte{}, nil, fmt.Errorf("failed to read plan: %w", err)
	}

	return target, planHash, plan, nil
}
