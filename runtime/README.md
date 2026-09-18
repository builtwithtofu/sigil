# Runtime Module

The `runtime` module provides the execution environment and runtime services for sigil commands and decorators.

## Purpose

This module contains the runtime infrastructure that powers sigil execution:

- **Execution Context**: Runtime environment for command execution
- **Decorator Registry**: Registration and lookup system for all decorators
- **Decorator Interfaces**: Abstract interfaces that all decorators must implement

## Key Components

### Execution Package (`runtime/execution/`)
- `context.go`: Execution context providing variables, shell execution, and decorator services
- `types.go`: Execution result types and execution mode definitions
- `shell_test.go`: Tests for shell execution functionality

### Decorators Package (`runtime/decorators/`)
- `interfaces.go`: Core decorator interfaces (FunctionDecorator, BlockDecorator, PatternDecorator)
- `registry.go`: Global decorator registration and lookup system

### Registry Package (`runtime/registry/`)
- Future home for additional registry services

## Module Dependencies

- **Depends on**: `core` module for AST types and plan structures
- **Used by**: `cli` module for decorator implementations and execution
- **External**: No external dependencies beyond Go standard library

## Architecture

### Execution Modes
1. **InterpreterMode**: Direct execution of commands
2. **GeneratorMode**: Code generation for standalone binaries  
3. **PlanMode**: Dry-run plan generation for visualization

### Decorator System
The runtime provides a unified decorator system where:
- **Value decorators** expand inline (e.g., `@var.name`, `@env.PORT`)
- **Execution decorators** wrap command blocks (e.g., `@exec.timeout {}`, `@exec.retry {}`, `@exec.parallel {}`)

### File output

See [file endpoint semantics](../docs/SPECIFICATION.md#85-file-endpoints-and-publication) for `>`, `>>`, and explicit atomic publication.

Registered output endpoints implement `core/decorator.Sink`; input capability is independently expressed by `Source`. `OpenWrite` returns an `Output` with `Write`, `Finish`, and `Abort`. Opening must acquire the destination before production. `Finish` publishes only successful output; `Abort` releases resources without publishing staging. Both must release resources on error. Return `decorator.ErrPublicationUnknown` when publication may have happened without acknowledgement; execution stops rather than automatically retrying.

`@file` delegates filesystem work to the selected session's optional `FileWriter` capability. Its internal modes distinguish truncate, append, and replace-on-success. Sessions without the requested operation fail closed. Endpoints borrow sessions and must not close them.

Plan execution and retained SDK execution trees share the output lifecycle. The older SDK sink interface is a compatibility boundary, not a second provider API: its streaming writers are adapted to `Output`, and its capabilities alias `decorator.IOCaps`. Do not adapt a publish-on-close writer as a streaming writer; implement `Output` explicitly.

## Usage

```go
import "github.com/builtwithtofu/sigil/runtime/execution"
import "github.com/builtwithtofu/sigil/runtime/decorators"

// Register a new decorator
decorators.RegisterFunction(&MyDecorator{})

// Create execution context
ctx := execution.NewContext(program)
```

## Design Principles

- **Mode-agnostic**: Decorators work across interpreter, generator, and plan modes
- **Extensible**: Easy to add new decorators without modifying core runtime
- **Safe**: Proper isolation and error handling for decorator execution
- **Performance**: Efficient execution with minimal overhead
