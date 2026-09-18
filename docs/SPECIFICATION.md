---
title: "Sigil Language Specification"
audience: "End Users and Script Authors"
summary: "Language semantics, execution contract, and operational guarantees"
---

# Sigil Language Specification

Sigil is a plan-first language for operational workflows.

Sigil authors write metaprogramming constructs that build an execution plan. The runtime executes shell commands and execution decorators from that plan.

See `docs/GRAMMAR.md` for formal EBNF syntax.

## 1. Language Boundary

Sigil has two semantic layers.

- **Metaprogramming layer**: `fun`, function calls, `var`, `if`, `for`, `when`
- **Execution layer**: shell commands, pipes, redirects, and execution decorators such as `@exec.retry`, `@exec.timeout`, and `@exec.parallel`

The metaprogramming layer builds a deterministic script structure. The execution layer performs the side effects.

This boundary is the core language meaning:

- script construction happens during planning
- side effects happen during execution
- contract verification compares planned structure and resolved values before execution when a plan contract is provided

## 2. Program Lifecycle

Sigil execution follows a stable lifecycle.

1. **Parse** source into a syntax/event tree.
2. **Plan** by resolving metaprogramming constructs and value dependencies.
3. **Resolve values** according to the selected mode.
4. **Execute** runtime nodes.
5. **Verify contract** when running with a resolved plan contract.

Two execution paths exist:

- **Direct run**: parse -> plan -> resolve -> execute
- **Contract run**: parse -> plan -> resolve -> compare with contract -> execute on match

A plan file is a verification target, not an executable artifact.

## 3. Program Forms and Modes

Sigil validation supports two top-level modes.

### 3.1 Script mode

Script mode accepts full language constructs:

- declarations
- metaprogramming statements
- shell execution
- decorator execution

### 3.2 Command mode

Command mode is definition-oriented:

- allows top-level `fun` declarations
- allows top-level `var` declarations
- rejects top-level shell execution
- rejects top-level function call execution

This mode models a task file where declarations define callable commands.

## 4. Variables, Values, and Types

## 4.1 Variable declaration

Sigil supports single and grouped declarations.

```sigil
var service = "api"

var (
    env = "prod"
    replicas = 3
)
```

## 4.2 Variable usage

Sigil variable references use decorator syntax.

```sigil
kubectl scale deployment/@var.service --replicas=@var.replicas
```

`${NAME}` is shell interpolation syntax and is not Sigil variable syntax.

## 4.3 Naming style

Variable names are case-sensitive identifiers.

Sigil does not enforce a casing convention. Teams choose their own naming style.

Documentation examples use `lower_snake_case` for consistency.

## 4.4 Type model

Core value categories:

- string
- int
- float
- bool
- duration
- array
- object
- none

Function parameters, struct fields, and decorator schemas define type contracts.

Optional function parameter types use `Type?` and accept `none`.

## 4.5 Expression semantics

Expression evaluation is deterministic.

- binary operators use standard precedence
- unary operators include logical negation and numeric sign
- prefix/postfix increment and decrement operate on expression operands according to parser grammar
- explicit cast operator uses `expr as Type` and `expr as Type?`

Absence semantics:

- `none` is the language absence literal
- non-optional typed parameters reject `none`
- optional typed parameters (`Type?`) accept `none`
- `none` and `as` are reserved language keywords

Arithmetic type rules:

- `int + int -> int`
- `float + float -> float`
- `int + float -> float`
- `duration + duration -> duration`

Division by zero fails planning.

## 4.6 Assignment semantics

Compound assignment operators:

- `+=`
- `-=`
- `*=`
- `/=`
- `%=`

Assignment updates the binding in scope.

## 5. Scope Semantics

Sigil uses lexical read visibility with isolation guarantees.

## 5.1 Block boundary semantics

Braces define block boundaries.

- `{ ... }` opens a nested scope
- declarations and mutations inside the block stay inside that block
- exiting the block restores the parent scope bindings

Blocks enclose state change to the block where the change appears.

This rule applies to metaprogramming blocks and execution blocks.

General rule:

- nested scopes read outer bindings
- declarations inside a nested block do not leak to the parent scope

Scope behavior by block category:

| Block category | Examples | Evaluation phase | Declaration leak |
|---|---|---|---|
| Metaprogramming blocks | `if`, `for`, `when`, `fun` body | plan phase | no |
| Execution blocks | `try`, `catch`, execution decorator blocks | runtime phase | no |

Execution blocks allow local mutation of inherited values inside the block while preserving the parent binding after block exit.

## 6. Functions (`fun`) as Plan-Time Templates

`fun` declarations define reusable templates.

### 6.1 Placement rule

`fun` declarations are top-level declarations.

Defining `fun` inside `if`, `for`, `when`, `try`, or decorator blocks fails parsing.

### 6.2 Declaration forms

```sigil
fun greet(name String) = echo "hello @var.name"

fun build(module String, target String = "dist") {
    @fs.workdir(@var.module) {
        npm ci
        npm run build --output=@var.target
    }
}
```

### 6.3 Parameter typing and defaults

Function parameters are typed.

- grouped form: `fun f(a, b String) { ... }`
- single form: `fun f(a String) { ... }`

Defaults attach to parameters and validate against declared types.

### 6.4 Struct declarations

`struct` declarations define reusable object shapes for typed function parameters.

```sigil
struct DeployConfig {
    env String
    retries Int = 3
    timeout Duration?
}

fun deploy(cfg DeployConfig) {
    echo "Deploying @var.cfg.env"
}
```

Struct declarations are top-level declarations.

- fields use `name Type` syntax
- optional fields use `Type?`
- defaults apply per field
- unknown object fields are rejected for struct-typed parameters
- optional self-references are allowed (for example `next Node?`)
- required recursive struct cycles are rejected

### 6.5 Enum declarations

`enum` declarations define global named value sets for typed function parameters and struct fields.

```sigil
enum DeployStage String {
    Dev
    Prod = "production"
}

fun deploy(stage DeployStage) {
    echo "Deploying @var.stage"
}
```

Enum declarations are top-level declarations.

- base type is optional and defaults to `String`
- enum base type uses `String` in this phase
- member forms are `Name` or `Name = "value"`
- implicit member values use the member name
- duplicate member names and duplicate member values are rejected
- enum members are referenced as `Type.Member` in expression contexts
- qualified enum references use exactly two segments in this phase (`Type.Member`)

Enum member references are plan-time constants.

```sigil
when @var.os {
    OS.Windows -> echo "running on Windows"
    OS.Linux -> echo "running on Linux"
}
```

### 6.6 Call semantics

Function calls use direct call syntax:

```sigil
build(module="cli")
```

Parser disambiguation:

- `name(...)` (no space before `(`) parses as a function call
- `name (...)` stays shell syntax for ordinary shell commands
- if `name` is a known Sigil function, `name (...)` is rejected and the parser tells the author to remove the space before `(`

### 6.7 Expansion semantics

Function calls expand during planning.

- arguments bind to parameter names
- defaults apply when omitted
- expanded bodies are inserted into the plan

Execution does not perform runtime function lookup.

### 6.8 Call graph constraints

Function calls must form an acyclic graph.

- direct recursion fails
- mutual recursion fails

Cycle errors include deterministic call-path traces.

## 7. Decorators

Decorators are namespaced operations invoked with `@`.

```sigil
@env.PORT(default=3000)
@exec.retry(times=3, delay=2s) { kubectl rollout status deployment/app }
```

Decorator name resolution uses a registry and prefers the longest registered path.

## 7.1 Decorator categories

- **Value decorators** return values for planning and substitution (`@var`, `@env`, secret/value providers)
- **Execution decorators** wrap execution behavior (`@exec.retry`, `@exec.timeout`, `@exec.parallel`, transport decorators)

## 7.2 Decorator argument binding

Binding model:

- named arguments bind by parameter name
- positional arguments bind to the next unbound slot
- slot order is required parameters first, then optional parameters
- named and positional arguments can mix in any order

Deterministic algorithm:

1. Build required-first slot order from schema.
2. Reserve all named argument targets.
3. Process arguments left-to-right.
4. Bind named args by name.
5. Bind positional args to next unbound and unreserved slot.
6. Fail on duplicate assignment or extra positional args.

## 7.3 Decorator block capability

Decorators declare block capability:

- block required
- block optional
- block forbidden

Parser enforces that capability during syntax validation.

## 7.4 Interpolation and decorator termination

Decorator references can appear in interpolated strings.

Sigil supports two interpolation forms inside double-quoted and backtick strings:

- simple inline form: `@var.name`, `@env.HOME`
- explicit braced form: `@{var.name}`, `@{env.HOME}`

`@{...}` is the full interpolation syntax. It explicitly bounds the interpolated reference and prevents following ASCII text from being parsed as part of the selector.

Use the braced form when adjacent text should remain literal:

```sigil
echo "@{var.service}_backup"
echo "@{env.HOME}/bin"
```

Unbraced interpolation remains valid for simple cases:

```sigil
echo "Deploying @var.service"
echo `HOME=@env.HOME`
```

Unbraced references continue to consume the full identifier after the dot. For example, `@var.name_suffix` refers to `name_suffix`, not `name` plus `_suffix`.

## 7.5 `@shell` shell selection

`@shell` accepts an optional `shell` argument.

- `shell="bash"`
- `shell="pwsh"`
- `shell="cmd"`

Resolution order:

1. `@shell(..., shell=...)`
2. session environment `OPAL_SHELL`
3. default `bash`

Shell support policy:

- Windows required: `pwsh` (PowerShell 7+)
- Windows best-effort: `cmd`, `bash`
- Unix targets: shell availability is target-dependent (`bash` default unless overridden)

Execution contract:

- Sigil preserves command text and passes it to the selected shell.
- Sigil does not normalize shell syntax across shells (env syntax, quoting, builtins, and path rules remain shell-native).
- Use explicit `shell=...` when command text depends on shell-specific syntax.
- Sigil-owned operators (`|`, `&&`, `||`, `;`, `>`, `>>`) are parsed and planned before shell execution.

## 8. Shell Execution Semantics

Shell commands are runtime operations.

The parser preserves shell argument boundaries through lexer spacing metadata.

Plan-time metaprogramming decides *which* commands run; runtime `@shell` decides *how* each command is executed via the selected shell.

### 8.1 Command composition

Shell chain operators:

- `|`
- `&&`
- `||`
- `;`

Redirect operators:

- `>`
- `>>`

### 8.2 Precedence

Shell precedence:

- `|` binds tighter than `&&` and `||`
- `&&` and `||` bind tighter than `;`
- newline acts as statement boundary with fail-fast sequencing between statements

### 8.3 Fail-fast line semantics

Block lines execute in order.

- newline-separated steps stop at first failure unless control flow/decorator policy changes behavior
- semicolon chaining follows shell continuation semantics

### 8.4 Operator ownership constraints

Operator semantics are runtime-contract semantics, not shell-dialect semantics.

- Sigil parses operator structure and builds execution trees independent of selected shell.
- Selected shell affects only leaf command execution (`command` text in `@shell`).
- Changing shell (`bash`, `pwsh`, `cmd`) does not change Sigil operator precedence or control flow behavior.

Future extensions may introduce shell-dialect-aware execution modes for specific transports, but this contract keeps operator ownership in Sigil.

### 8.5 File endpoints and publication

Redirects attach to commands: `a | b > result.txt` redirects `b`, not a new grouped pipeline operation. Bare paths lower to `@file` before plan display and hashing:

```sigil
printf report > "daily report.txt"
printf report > @file("daily report.txt")
printf entry >> events.log
printf artifact > @file("artifact.txt", atomic=true, perm=384)
```

The first two declarations are equivalent, including literal backslashes in quoted paths. `perm` is the creation mode as an integer (`384` is octal `0600`); its default is `420` (`0644`). For variables, use `> "@var.destination"` or `> @file(@var.destination)`. Whole-value option references retain their types, such as `atomic=@var.publish` and `perm=@var.permissions`.

- `>` opens and truncates before starting its producer; `>>` opens for streaming append. Neither buffers the entire output nor creates missing parent directories.
- Default output streams immediately. Producer failure or cancellation can leave partial output. Existing streaming files retain their permissions; new files use the requested mode subject to the filesystem's creation mask.
- Local POSIX FIFO output waits for a reader. Cancellation interrupts that wait and blocked FIFO writes. Non-pollable filesystem calls remain subject to operating-system blocking behavior.
- Open failures prevent the owning producer from starting. Write, short-write, and completion failures fail the redirect even if the producer otherwise succeeds. When production and cleanup both fail, both outcomes are reported.
- `atomic=true` requests replacement only after successful production, including empty output. Local POSIX regular files use private same-directory staging and rename. Failure or cancellation removes unpublished staging. Each retry attempt starts afresh.
- Atomic replacement rejects append, symlink destinations, and non-regular existing destinations. It replaces inode metadata rather than preserving ownership, ACLs, or existing permissions. Requested permissions apply to the replacement. It does not promise crash durability or cleanup after process/machine death.
- SSH output streams on the selected remote session, using its working directory. Atomic replacement, executable creation permission bits, and Windows SSH output are currently unsupported and fail before production. Unsupported backends never fall back to controller files or silently downgrade publication policy.
- Publication without a confirmed acknowledgement is reported as **outcome unknown** and stops automatic retry. Reconcile the destination before replaying the operation.

Plans show effective endpoint arguments and context identifiers, with the transport configuration and reviewed local working directory. Scope wrappers such as `@fs.workdir` remain part of that declaration. Planning does not open destinations. Changing the destination, scope, permissions, operator, or publication policy changes the contract; changing destination contents alone does not. These semantics require plan format version 2; regenerate older contracts.

Plans warn that append inside `@exec.retry` can duplicate output. Identical known file destinations in independent `@exec.parallel` branches or pipeline commands are rejected. These are bounded checks on declarations, not filesystem alias detection, locking against external writers, deterministic interleaving, or exactly-once delivery. Distinct declared destinations remain usable.

Sink payloads remain raw, including binary data and secrets. Terminal and plan output retain the CLI's secret-scrubbing boundary. No new sink providers, output grouping syntax, or numbered-descriptor redirection is implied by this contract.

## 9. Control Flow Semantics

## 9.1 `if`

`if` conditions evaluate during planning.

- only the selected branch expands into runtime steps
- unselected branch is pruned from execution plan

## 9.2 `for`

`for` loops unroll during planning.

- each collection element produces one iteration expansion
- iteration order follows collection order
- empty collection produces zero expanded steps

## 9.3 `when`

`when` performs first-match branch selection during planning.

Supported patterns include:

- literal string matches
- OR patterns
- regex patterns (`r"..."`)
- numeric range patterns (`a...b`)
- `else` catch-all

## 9.4 `try/catch/finally`

`try/catch/finally` remains a runtime construct.

- plan records potential paths
- runtime executes `try` or `catch`
- runtime executes `finally` after branch completion

## 9.5 `@exec.parallel`

`@exec.parallel` runs branch blocks concurrently with isolation.

Branch guarantees:

- branch-local scope isolation
- deterministic merged output order by plan order
- failure behavior governed by decorator policy

## 10. Environment and Transport Semantics

`@env` is session-scoped.

- in local session, `@env.X` reads local process environment snapshot
- in transport session, `@env.X` reads transport session environment
- `$X` inside shell reads shell session environment at command execution

`@env.X` and `$X` read from the same session boundary but at different semantic phases.

### 10.1 Transport boundary rule

Values do not cross transport boundaries implicitly.

To pass a value across a boundary, bind it explicitly:

```sigil
var deployer = @env.USER
@ssh.connect(host="example", env={"DEPLOYER": @var.deployer}) {
    echo $DEPLOYER
}
```

### 10.2 Root-only decorator rule

Root-only value decorators fail inside transport-switching contexts.

Use one of:

- shell variables inside transport shell commands
- hoisting at root and passing via explicit parameters or env mapping

### 10.3 Transport categories

Transports fall into three categories:

**Connectivity transports** change execution location:
- `@ssh.connect` - Execute on remote server
- `@docker.exec` - Execute in container
- `@k8s.exec` - Execute in Kubernetes pod

These transports implement network dialing through the parent context. Nested transports dial via their parent, not directly from the local machine.

**Isolation transports** constrain execution at the current location:
- `@isolated` - Linux namespace isolation
- `@sandbox` - Subprocess sandboxing

These transports do not change location. They apply security constraints to the current context.

**Local transport** is the baseline:
- Direct execution on the host
- Full network and filesystem access
- All execution starts in local transport

### 10.4 Transport nesting rules

Transport nesting follows security-preserving rules:

| Parent | Child | Allowed | Reason |
|--------|-------|---------|--------|
| Connectivity | Connectivity | Yes | Child dials through parent context |
| Connectivity | Isolation | Yes | Inherited connection with local constraints |
| Isolation | Connectivity | No | Cannot grant network from isolated context |
| Isolation | Isolation | Yes | Policies compose (intersection) |

**Valid nesting example:**
```sigil
@ssh.connect(host="bastion") {
    @isolated(network="deny") {
        # SSH out allowed through bastion, but no direct internet
    }
}
```

**Blocked nesting example:**
```sigil
@isolated(network="deny") {
    @ssh.connect(host="server") {
        # ERROR: Cannot establish SSH from network-isolated context
    }
}
```


## 11. Planning Modes and Execution Modes

Sigil supports four operational modes.

| Mode | Invocation shape | Resolution behavior | Primary purpose |
|---|---|---|---|
| Quick plan | `--dry-run` | cheap-only resolution; expensive values deferred | fast structural preview |
| Resolved plan | `--dry-run --resolve` | full resolution | contract generation and review |
| Direct execution | execute source | fresh resolution then execute | immediate run |
| Contract execution | `run --plan <file>` | fresh resolution + contract comparison | verified execution |

## 11.1 Quick plan

Quick plan expands control flow and shows planned path while deferring expensive value sources.

## 11.2 Resolved plan

Resolved plan materializes all value decorators and emits a deterministic contract artifact.

## 11.3 Direct execution

Direct execution plans and resolves from source, then executes without external contract comparison.

## 11.4 Contract execution

Contract execution performs replan + resolve and compares against a provided contract artifact before execution.

## 12. Contract Verification Semantics

Contract verification compares fresh planning output against a reviewed contract.

Verification flow:

1. parse and plan from source
2. resolve values
3. compare structure and hashed placeholders
4. execute on match
5. fail on mismatch with actionable diff

Mismatch classes:

- source drift
- environment drift
- infrastructure drift
- non-deterministic value behavior

## 12.1 Plan hash scope

Hash scope includes execution-relevant structure and resolved placeholder mapping.

Hash scope excludes ephemeral run telemetry.

## 13. Security and Data Handling

## 13.1 Placeholder model

Resolved values in plans and logs use opaque placeholders.

Security invariant:

- plans do not expose raw secret values
- logs do not expose raw secret values

## 13.2 Scrubbing boundaries

Scrubbing applies to:

- plan artifacts
- terminal/log output paths

Raw values continue to flow through runtime shell pipes and file redirects unless explicit scrub operations are applied before output sinks.

## 13.3 Deterministic identifiers

Placeholder IDs are deterministic within one contract context and vary across independent contract contexts.

This property supports verification and resists cross-context correlation.

## 14. Determinism and Idempotency

Determinism guarantees:

- same source plus same resolved inputs yields same plan structure
- dead branches do not evaluate value decorators
- branch pruning is deterministic

Resolved-plan requirement:

- value decorators used in resolved contracts must behave referentially transparent for the contract scope

Idempotency model:

- scripts express desired operational outcomes
- reruns converge without requiring external state files
- drift detection relies on fresh resolution and contract comparison behavior

## 15. Error Semantics

Error precedence rule:

1. `err != nil` means failure
2. `err == nil` means success only when exit code is `0`

Typed execution errors support policy behavior:

- retryable error
- timeout error
- cancelled error

Decorator policy decides handling strategy for each type.

## 16. Common Authoring Errors

### 16.1 Defining `fun` inside control flow

Invalid:

```sigil
for module in ["cli", "runtime"] {
    fun build_module(module String) = npm run build
}
```

Valid:

```sigil
fun build_module(module String) = npm run build

for module in ["cli", "runtime"] {
    build_module(module=@var.module)
}
```

### 16.2 Using shell interpolation syntax for Sigil variables

Invalid:

```sigil
var service = "api"
kubectl scale deployment/${service} --replicas=3
```

Valid:

```sigil
var service = "api"
kubectl scale deployment/@var.service --replicas=3
```

### 16.3 Duplicate decorator parameter assignment

Invalid:

```sigil
@exec.retry(times=3, times=5) { echo "x" }
```

Valid:

```sigil
@exec.retry(3, delay=2s) { echo "x" }
@exec.retry(delay=2s, 3) { echo "x" }
```

### 16.4 Missing decorator terminator inside string interpolation

Invalid:

```sigil
echo "@var.name_suffix"
```

Valid:

```sigil
echo "@{var.name}_suffix"
```

### 16.5 Non-deterministic value in resolved contract

Invalid for resolved contract generation:

```sigil
var stamp = @time.now()
```

Use deterministic inputs or seeded deterministic decorators for contract-safe workflows.

## 17. End-to-End Example

```sigil
var (
    env = @env.ENV(default="development")
    version = @env.VERSION(default="latest")
    modules = ["api", "worker", "ui"]
)

fun deploy_module(module String, replicas Int = 1) {
    @exec.retry(times=3, delay=5s) {
        kubectl set image deployment/@var.module app=@var.version
        kubectl scale deployment/@var.module --replicas=@var.replicas
        kubectl rollout status deployment/@var.module --timeout=300s
    }
}

fun deploy_all {
    when @var.env {
        "production" -> {
            for module in @var.modules {
                deploy_module(module=@var.module, replicas=3)
            }
        }
        else -> {
            for module in @var.modules {
                deploy_module(module=@var.module)
            }
        }
    }
}

deploy_all()
```

Meaning of this example:

- metaprogramming resolves `when` and `for` during planning
- function calls expand into concrete runtime steps
- execution decorator policy wraps shell execution
- runtime executes deterministic step order from the selected branch

## 18. Glossary

- **Plan**: deterministic execution structure produced from source and resolved values.
- **Contract**: reviewed resolved plan artifact used as verification target.
- **Metaprogramming construct**: syntax that shapes plan structure during planning.
- **Execution construct**: syntax that performs runtime side effects.
- **Value decorator**: decorator that yields a value for substitution.
- **Execution decorator**: decorator that wraps execution behavior.
- **Transport boundary**: session switch where environment and execution context change.
