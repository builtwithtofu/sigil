package executor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/builtwithtofu/sigil/core/planfmt"
	"github.com/stretchr/testify/require"
)

func runFileRedirect(t *testing.T, command, path string, mode planfmt.RedirectMode, args ...planfmt.Arg) (*ExecutionResult, string) {
	t.Helper()
	args = append(args, planfmt.Arg{Key: "path", Val: planfmt.Value{Kind: planfmt.ValueString, Str: path}})
	p := &planfmt.Plan{Steps: []planfmt.Step{{ID: 1, Tree: &planfmt.RedirectNode{
		Source: shellCmd(command), Target: planfmt.EndpointSpec{Decorator: "@file", Args: args}, Mode: mode,
	}}}}
	var stderr bytes.Buffer
	result, err := ExecutePlan(context.Background(), p, Config{Stderr: &stderr}, testVault())
	require.NoError(t, err)
	return result, stderr.String()
}

func TestFileRedirectOpenFailurePreventsProducer(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "producer-ran")
	result, diagnostics := runFileRedirect(t, "touch '"+marker+"'", filepath.Join(dir, "missing", "out"), planfmt.RedirectOverwrite)
	require.NotZero(t, result.ExitCode, diagnostics)
	_, err := os.Stat(marker)
	require.True(t, os.IsNotExist(err), "producer ran despite missing destination parent")
}

func TestFileRedirectStreamsAndHonoursPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	result, diagnostics := runFileRedirect(t, "printf partial; exit 23", path, planfmt.RedirectOverwrite,
		planfmt.Arg{Key: "perm", Val: planfmt.Value{Kind: planfmt.ValueInt, Int: 0o600}})
	require.Equal(t, 23, result.ExitCode, diagnostics)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "partial", string(data))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	result, diagnostics = runFileRedirect(t, "printf tail", path, planfmt.RedirectAppend)
	require.Zero(t, result.ExitCode, diagnostics)
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "partialtail", string(data))
	result, diagnostics = runFileRedirect(t, "true", path, planfmt.RedirectOverwrite)
	require.Zero(t, result.ExitCode, diagnostics)
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Empty(t, data)
}

func TestFileRedirectPreservesBinaryPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binary")
	result, diagnostics := runFileRedirect(t, `printf '\000\377\012'`, path, planfmt.RedirectOverwrite)
	require.Zero(t, result.ExitCode, diagnostics)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, []byte{0, 255, 10}, data)
}
