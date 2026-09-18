---
oep: 016
title: Output Redirection and Sink Architecture
status: Draft
type: Feature
created: 2025-01-23
updated: 2025-01-23
---

# OEP-016: Output Redirection and Sink Architecture

> Historical proposal, not the current contract. See [file endpoint semantics](../SPECIFICATION.md#85-file-endpoints-and-publication): output streams by default, and atomic replacement is explicit. The S3/HTTP providers and broader capabilities below are not implemented by this work.

## Summary

Add POSIX-style output redirection (`>` and `>>`) to Opal with a sink architecture that enables redirecting command output to files, S3 objects, HTTP endpoints, and other destinations. The sink abstraction ensures atomic writes, capability checking, and transport-aware execution (local/SSH/Docker).

## Motivation

### The Problem

Currently, Opal has no way to redirect command output to files or other destinations:

```opal
// ❌ DOESN'T WORK: No output redirection
kubectl logs api --since=1h  // Output goes to stdout, can't save to file
```

Users must work around this limitation:

```opal
// ❌ WORKAROUND: Use shell wrapper
@shell("kubectl logs api --since=1h > logs/api.log")  // Loses Opal's safety features
```

This breaks Opal's composability and forces users back to raw shell commands.

### Use Cases

**Use Case 1: Save command output to file**
```opal
kubectl logs api --since=1h > logs/api.log
```

**Use Case 2: Append to log file**
```opal
kubectl logs api --since=1h >> logs/api.log
```

**Use Case 3: Redirect to S3 (future)**
```opal
kubectl logs api --since=1h > @aws.s3.object("logs/api.log")
```

**Use Case 4: Complex pipelines with redirection**
```opal
kubectl logs api | grep ERROR > logs/errors.log
(echo "start" && kubectl logs api) > logs/full.log
```

**Use Case 5: Remote execution with redirection**
```opal
@ssh.connect(host="web01") {
    kubectl logs api > /var/log/api.log  // File created on remote machine
}
```

## Proposal

### Syntax

**Basic redirection:**
```opal
command > file.txt   // Overwrite (truncate file)
command >> file.txt  // Append to file
```

**With variables:**
```opal
echo "data" > @var.OUTPUT_FILE
```

**With pipes:**
```opal
cat data.txt | grep "error" > errors.txt
```

**With operators:**
```opal
(echo "a" && echo "b") > output.txt  // Both outputs go to file
(echo "a"; echo "b"; echo "c") > output.txt  // All outputs go to file
```

**Future: Decorator sinks**
```opal
kubectl logs api > @aws.s3.object("logs/api.log")
kubectl logs api > @http.post("https://logs.example.com/ingest")
```

### Core Restrictions

#### Restriction 1: Redirect Must Appear at End of Command

Redirection must be the last operator in a command:

```opal
// ✅ CORRECT: Redirect at end
echo "hello" > file.txt
echo "hello" | grep hello > file.txt

// ❌ FORBIDDEN: Redirect in middle
echo "hello" > file.txt | grep hello  // Parse error
```

**Why?** Matches POSIX shell behavior and simplifies parsing. The redirect operator has lower precedence than pipes but higher than `&&`/`||`.

**Operator precedence (high to low):**
```
| (pipe) > redirect > && (and) > || (or) > ; (semicolon)
```

#### Restriction 2: Only Stdout Redirection (MVP)

Only stdout (file descriptor 1) is redirected:

```opal
// ✅ CORRECT: Stdout redirection
command > output.txt

// ❌ NOT YET SUPPORTED: Stderr redirection
command 2> errors.txt  // Future feature
command &> all.txt     // Future feature
```

**Why?** MVP focuses on the common case (stdout). Stderr redirection can be added later without breaking changes.

#### Restriction 3: No Nested Redirects

Cannot redirect the output of a redirect:

```opal
// ❌ FORBIDDEN: Nested redirect
(echo "hello" > file1.txt) > file2.txt  // Parse error or runtime error
```

**Why?** Semantics are unclear. What would this even mean? The first redirect writes to file1.txt and produces no stdout.

### Semantics

#### POSIX-Compatible Behavior

Opal follows bash semantics for redirection:

**Overwrite (`>`):**
- Truncates file to zero length (or creates if doesn't exist)
- Atomic write via temp file + rename (readers never see partial writes)
- File opened before command execution (POSIX semantics)

**Append (`>>`):**
- Appends to file (or creates if doesn't exist)
- Direct append (not atomic, matches POSIX)
- File opened before command execution

**Operator behavior:**
```bash
# In bash (and Opal):
(echo "a" && echo "b") > file.txt  # Both outputs go to file
(echo "a"; echo "b") > file.txt    # Both outputs go to file
echo "a" | grep a > file.txt       # Pipeline output goes to file
```

#### Transport-Aware Execution

Redirection respects the current execution context:

```opal
// Local execution
echo "hello" > file.txt  // Creates local file

// SSH execution
@ssh.connect(host="remote") {
    echo "hello" > file.txt  // Creates file on remote machine
}

// Docker execution
@docker.exec(container="app") {
    echo "hello" > file.txt  // Creates file inside container
}
```

**How it works:** The sink uses the current context's transport to open the file. LocalTransport opens local files, SSHTransport opens remote files via SSH, etc.

#### Atomic Writes (Overwrite Mode)

Overwrite mode (`>`) uses atomic writes to prevent partial writes:

```opal
// Atomic write process:
// 1. Write to temp file: output.txt.opal.tmp
// 2. Close temp file
// 3. Rename temp to final: output.txt.opal.tmp → output.txt
//
// Readers see:
// - Old content (before rename)
// - New content (after rename)
// - NEVER partial content (during write)
```

**Why atomic?** Prevents corrupted files if process crashes mid-write. Critical for production systems.

### Examples

**Example 1: Save logs to file**
```opal
target deploy {
    kubectl apply -f k8s/
    kubectl logs -f deployment/api > logs/deploy-@var.timestamp.log
}
```

**Example 2: Append to audit log**
```opal
target audit {
    echo "@var.timestamp: Deployed version @var.version" >> audit.log
}
```

**Example 3: Error logs**
```opal
target check {
    kubectl logs api --since=1h | grep ERROR > logs/errors.log
}
```

**Example 4: Complex pipeline**
```opal
target report {
    (
        echo "=== Deployment Report ===" &&
        kubectl get pods &&
        kubectl get services
    ) > reports/deployment-@var.date.txt
}
```

**Example 5: Remote execution**
```opal
target remote-logs {
    @ssh.connect(host="web01") {
        kubectl logs api --since=1h > /var/log/api-recent.log
    }
}
```

## Rationale

### Why Sink Abstraction?

**Decision:** Redirect targets are "sinks" (destinations), not commands to execute.

**Alternatives considered:**
1. Treat `> file.txt` as executing `@shell("file.txt")` with special flag
2. Add redirect as a command modifier (like stdin/stdout pipes)
3. Use sink abstraction (chosen)

**Why sink abstraction?**
- **Clarity:** Redirect targets are destinations, not commands
- **Extensibility:** Easy to add S3, HTTP, compression sinks
- **Transport-aware:** Sinks use current context's transport automatically
- **Capability checking:** Sinks declare what operations they support (>, >>)
- **No special cases:** `@shell` stays untouched, no redirect-specific logic

**Tradeoffs accepted:**
- More abstraction layers (Sink interface, FsPathSink, etc.)
- Slightly more complex planner (converts targets to sinks)

### Why Atomic Writes for Overwrite?

**Decision:** Use temp file + rename for `>` operator.

**Why?**
- **Safety:** Readers never see partial writes
- **Production-ready:** Critical for logs, configs, data files
- **POSIX-compatible:** Matches common Unix tools (rsync, package managers)

**Tradeoffs accepted:**
- Extra disk I/O (write temp, then rename)
- Requires temp file space (usually same filesystem)

**Not atomic for append:** POSIX doesn't guarantee atomic appends, so we match that behavior.

### Why Bash Operator Semantics?

**Decision:** `(cmd1 && cmd2) > file` redirects BOTH commands' output.

**Alternatives considered:**
1. Only redirect last command: `(cmd1 && cmd2) > file` → only cmd2 goes to file
2. Redirect all commands (chosen)

**Why redirect all?**
- **Matches bash:** No surprises for shell users
- **Intuitive:** Users expect subshell-like behavior
- **Composable:** Works naturally with operators

**Verified with bash:**
```bash
(echo "a" && echo "b") > file.txt  # Both go to file ✓
(echo "a"; echo "b") > file.txt    # Both go to file ✓
```

### Why Operator Precedence: `| > redirect > && > ||`?

**Decision:** Redirect has lower precedence than pipe, higher than `&&`/`||`.

**Why?**
- **Matches POSIX:** Standard shell behavior
- **Intuitive:** `cmd1 | cmd2 > file` means "pipe then redirect"
- **Composable:** `cmd1 && cmd2 > file` means "both outputs to file"

**Examples:**
```opal
echo "a" | grep a > file.txt       // (echo | grep) > file
echo "a" > file.txt && echo "b"    // (echo > file) && echo
```

## Alternatives Considered

### Alternative 1: Shell Wrapper Only

**Approach:** No native redirection, use `@shell` wrapper:
```opal
@shell("kubectl logs api > logs/api.log")
```

**Rejected because:**
- Loses Opal's safety features (secret scrubbing, error handling)
- Breaks composability (can't use with decorators)
- Not transport-aware (doesn't work with `@ssh.connect`)
- Forces users back to raw shell

### Alternative 2: Redirect as Command Modifier

**Approach:** Add redirect as a property of commands:
```opal
@shell("kubectl logs api", redirect="> logs/api.log")
```

**Rejected because:**
- Not composable (can't use with pipes)
- Doesn't match user expectations (shell-like syntax)
- Requires special handling in every decorator
- Breaks "everything is a decorator" philosophy

### Alternative 3: Decorator-Only Sinks

**Approach:** Only support decorator sinks, no file paths:
```opal
kubectl logs api > @file("logs/api.log")
kubectl logs api > @aws.s3.object("logs/api.log")
```

**Rejected because:**
- Verbose for common case (files)
- Doesn't match shell expectations
- Harder to learn (extra decorator syntax)

**Chosen approach:** Support both file paths (MVP) and decorator sinks (future).

### Alternative 4: Non-Atomic Writes

**Approach:** Direct write to file (no temp file):
```opal
echo "data" > file.txt  // Write directly, no temp file
```

**Rejected because:**
- Readers can see partial writes (corrupted files)
- Not production-ready for critical files
- Hard to add atomicity later (breaking change)

**Chosen approach:** Atomic writes for overwrite, document append as non-atomic.

## Implementation

### Phase 1: MVP - File Path Sinks ✅ COMPLETE

**Deliverables:**
- [x] Sink interface and FsPathSink implementation
- [x] Transport.OpenFileWriter() for local files
- [x] Atomic writes via temp file + rename
- [x] Parser support for `>` and `>>` operators
- [x] Planner converts file paths to FsPathSink
- [x] Executor opens sinks and wires stdout
- [x] Support all tree node types (pipes, operators)
- [x] Tests for basic redirection
- [x] Tests for complex sources (pipes, operators)
- [x] Tests for atomic writes

**Status:** Complete. All tests passing.

### Phase 2: SSH/Docker Transport Support

**Deliverables:**
- [ ] SSHTransport.OpenFileWriter() - opens remote files via `bash -c "cat > path"`
- [ ] DockerTransport.OpenFileWriter() - opens files inside container
- [ ] Tests for remote redirection
- [ ] Documentation for remote execution

**Estimated effort:** 4-6 hours

### Phase 3: Decorator Sinks (S3, HTTP, etc.)

**Deliverables:**
- [ ] DecoratorSink wrapper for lazy evaluation
- [ ] @aws.s3.object decorator (dual-purpose: read and write)
- [ ] S3Sink with multipart upload
- [ ] S3 atomic append via multipart compose
- [ ] Tests for S3 redirection
- [ ] Documentation for cloud sinks

**Estimated effort:** 10-15 hours

**Open question:** Should `@aws.s3.object` be dual-purpose (read and write) or separate decorators?

### Phase 4: Advanced Features (Future)

**Potential features:**
- [ ] Stderr redirection (`2>`, `&>`)
- [ ] Multi-sink tee (`@stream.tee`)
- [ ] Compression sinks (`@compress.gzip`)
- [ ] HTTP sinks (`@http.post`)
- [ ] GCS/Azure sinks

## Compatibility

### Breaking Changes

None. This is a new feature.

### Migration Path

N/A - new feature, no migration needed.

## Open Questions

### Question 1: Should decorator sinks be dual-purpose?

**Context:** `@aws.s3.object("key")` could be used as:
- **Sink (write):** `echo "data" > @aws.s3.object("key")`
- **Source (read):** `@aws.s3.object("key") | grep pattern`

**Options:**
1. **Dual-purpose** - same decorator for read and write
2. **Separate decorators** - `@aws.s3.read` and `@aws.s3.write`

**Leaning toward:** Dual-purpose (matches user expectations, less duplication)

**Needs:** Community feedback on decorator design patterns

### Question 2: How to handle decorator sink evaluation?

**Context:** Decorator sinks need runtime context (AWS credentials, etc.) but are evaluated at redirect time.

**Options:**
1. **Lazy evaluation** - store decorator call, evaluate when opening
2. **Plan-time evaluation** - evaluate during planning (may fail without credentials)
3. **Two-phase evaluation** - validate at plan time, execute at runtime

**Leaning toward:** Lazy evaluation (most flexible)

**Needs:** Implementation experience to validate approach

### Question 3: Should we support stderr redirection?

**Context:** POSIX shells support `2>` for stderr, `&>` for both.

**Options:**
1. **MVP only stdout** - add stderr later if needed
2. **Full POSIX support** - implement `2>` and `&>` now

**Leaning toward:** MVP only stdout (can add later without breaking changes)

**Needs:** User feedback on whether stderr redirection is critical

### Question 4: Should append mode be atomic?

**Context:** Current implementation uses direct append (not atomic). Could use multipart compose for atomicity.

**Options:**
1. **Non-atomic append** - matches POSIX, simpler implementation
2. **Atomic append** - safer but more complex (temp file + cat + rename)

**Leaning toward:** Non-atomic (matches POSIX, document limitation)

**Needs:** User feedback on whether atomic append is worth the complexity

### Question 5: How to handle concurrent appends?

**Context:** Multiple processes appending to same file can interleave writes.

**Options:**
1. **No coordination** - document as unsafe (current)
2. **File locking** - use flock/fcntl for coordination
3. **Sink capability** - sinks declare ConcurrentSafe=true/false

**Leaning toward:** Sink capability (explicit, extensible)

**Needs:** Real-world use cases to validate approach

## References

### Related OEPs
- OEP-011: System Shell - Defines shell operator semantics
- OEP-001: Runtime Let Binding - Shows decorator composition patterns

### External Inspiration
- **Bash redirection** - POSIX shell redirection semantics
- **Terraform providers** - Resource abstraction pattern
- **Pulumi outputs** - Lazy evaluation of resources
- **AWS S3 multipart upload** - Atomic append via compose

### Implementation References
- `core/sdk/execution.go` - Sink interface and FsPathSink
- `core/sdk/executor/transport.go` - Transport.OpenFileWriter()
- `runtime/executor/executor.go` - Redirect execution logic
- `runtime/planner/tree_builder.go` - Redirect operator precedence

---

## Status History

- **2025-01-23:** Draft - Initial proposal with MVP complete
- **TBD:** Accepted - After community review
- **TBD:** Implemented - After Phase 2 and 3 complete
