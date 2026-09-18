package decorators

import (
	"context"
	"fmt"
	"regexp"

	"github.com/builtwithtofu/sigil/core/decorator"
)

// ShellDecorator implements the @shell decorator using the new decorator architecture.
// It executes shell commands via Session.Run() using explicit shell selection.
type ShellDecorator struct{}

// Descriptor returns the decorator metadata.
func (d *ShellDecorator) Descriptor() decorator.Descriptor {
	return decorator.NewDescriptor("shell").
		Summary("Execute shell commands").
		ParamString("command", "Shell command").
		Required().
		Examples("echo hello", "npm run build", "/path/to/file.txt").
		Done().
		ParamEnum("shell", "Command shell override").
		Values("bash", "pwsh", "cmd").
		Examples("bash", "pwsh", "cmd").
		Done().
		Block(decorator.BlockForbidden).             // Leaf decorator - no blocks
		TransportScope(decorator.TransportScopeAny). // Works in any session
		Roles(decorator.RoleWrapper).
		Build()
}

// Wrap implements the Exec interface.
// @shell is a leaf decorator - it ignores the 'next' parameter and executes directly.
func (d *ShellDecorator) Wrap(next decorator.ExecNode, params map[string]any) decorator.ExecNode {
	return &shellNode{params: params}
}

// shellNode wraps shell command execution.
type shellNode struct {
	params map[string]any
}

type shellConfig struct {
	Command string `decorator:"command"`
	Shell   string `decorator:"shell"`
}

// Execute implements the ExecNode interface.
// Executes the shell command via Session.Run().
func (n *shellNode) Execute(ctx decorator.ExecContext) (decorator.Result, error) {
	cfg, _, err := decorator.DecodeInto[shellConfig](
		(&ShellDecorator{}).Descriptor().Schema,
		nil,
		n.params,
	)
	if err != nil {
		return decorator.Result{ExitCode: decorator.ExitFailure}, err
	}

	command := cfg.Command

	// INVARIANT: Command must not contain unresolved DisplayIDs
	// DisplayIDs should be resolved to actual values before execution
	// Format: sigil:<base64url> where base64url is 22 chars [A-Za-z0-9_-]
	// Use regex to avoid false positives (e.g., "Documentation for sigil: see docs/")
	displayIDPattern := regexp.MustCompile(`sigil:[A-Za-z0-9_-]{22}`)
	if displayIDPattern.MatchString(command) {
		panic(fmt.Sprintf("INVARIANT VIOLATION: Command contains unresolved DisplayID: %s\n"+
			"DisplayIDs must be resolved to actual values before execution.\n"+
			"Format: sigil:<base64url-hash> (22 chars)\n"+
			"This indicates the executor is not resolving secrets from the plan.", command))
	}

	// Use parent context for cancellation and deadlines
	// If no context provided, use background
	execCtx := ctx.Context
	if execCtx == nil {
		execCtx = context.Background()
	}

	shellName, err := resolveShellName(cfg.Shell, ctx.Session)
	if err != nil {
		return decorator.Result{ExitCode: decorator.ExitFailure}, err
	}

	argv, err := shellCommandArgs(shellName, command)
	if err != nil {
		return decorator.Result{ExitCode: decorator.ExitFailure}, err
	}

	// Configure I/O from ExecContext
	opts := decorator.RunOpts{
		Stdin:  ctx.Stdin,  // Piped input (nil if not piped)
		Stdout: ctx.Stdout, // Piped output (nil if not piped)
		Stderr: ctx.Stderr, // Forward stderr
	}

	result, err := ctx.Session.Run(execCtx, argv, opts)

	return result, err
}

func resolveShellName(explicit string, session decorator.Session) (string, error) {
	if explicit != "" {
		return explicit, nil
	}

	if session != nil {
		if envShell := session.Env()["OPAL_SHELL"]; envShell != "" {
			if _, err := shellCommandArgs(envShell, ""); err != nil {
				return "", fmt.Errorf("invalid OPAL_SHELL %q: expected one of bash, pwsh, cmd", envShell)
			}
			return envShell, nil
		}
	}

	return "bash", nil
}

func shellCommandArgs(shellName, command string) ([]string, error) {
	switch shellName {
	case "bash":
		return []string{"bash", "-c", command}, nil
	case "pwsh":
		return []string{"pwsh", "-NoProfile", "-NonInteractive", "-Command", command}, nil
	case "cmd":
		return []string{"cmd", "/C", command}, nil
	default:
		return nil, fmt.Errorf("unsupported shell %q: expected one of bash, pwsh, cmd", shellName)
	}
}

// Register @shell decorator
func init() {
	// Register with decorator registry
	if err := decorator.Register("shell", &ShellDecorator{}); err != nil {
		panic(fmt.Sprintf("failed to register @shell decorator: %v", err))
	}
}
