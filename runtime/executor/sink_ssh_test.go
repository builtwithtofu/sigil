package executor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/builtwithtofu/sigil/core/decorator"
	"github.com/builtwithtofu/sigil/core/planfmt"
	"github.com/stretchr/testify/require"
)

func TestSSHFileRedirectUsesSessionFilesystem(t *testing.T) {
	server := decorator.StartSSHTestServer(t)
	if server == nil {
		t.Skip("local SSH fixture unavailable")
	}
	defer server.Stop()
	session, err := decorator.NewSSHSession(map[string]any{"host": "127.0.0.1", "port": server.Port, "user": "test", "key": server.ClientKey, "strict_host_key": false})
	require.NoError(t, err)
	defer session.Close()
	dir := t.TempDir()
	remote := session.WithWorkdir(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run := func(command, path string, mode planfmt.RedirectMode, args ...planfmt.Arg) (*ExecutionResult, string) {
		args = append(args, planfmt.Arg{Key: "path", Val: planfmt.Value{Kind: planfmt.ValueString, Str: path}})
		producer := shellCmd(command)
		producer.TransportID = "remote"
		p := &planfmt.Plan{Steps: []planfmt.Step{{ID: 1, Tree: &planfmt.RedirectNode{Source: producer, Target: planfmt.EndpointSpec{Decorator: "@file", TransportID: "remote", Args: args}, Mode: mode}}}}
		p.Transports = []planfmt.Transport{{ID: "remote", Decorator: "local"}}
		var stderr bytes.Buffer
		result, err := ExecutePlan(ctx, p, Config{Stderr: &stderr, sessionFactory: func(string) (decorator.Session, error) { return remote, nil }}, testVault())
		require.NoError(t, err)
		return result, stderr.String()
	}
	result, diagnostics := run("printf partial; exit 23", "artifact", planfmt.RedirectOverwrite, planfmt.Arg{Key: "perm", Val: planfmt.Value{Kind: planfmt.ValueInt, Int: 0o600}})
	require.Equal(t, 23, result.ExitCode, diagnostics)
	result, diagnostics = run("printf tail", "artifact", planfmt.RedirectAppend)
	require.Zero(t, result.ExitCode, diagnostics)
	data, err := os.ReadFile(filepath.Join(dir, "artifact"))
	require.NoError(t, err)
	require.Equal(t, "partialtail", string(data))
	info, err := os.Stat(filepath.Join(dir, "artifact"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	result, diagnostics = run("touch producer-ran", "missing/child", planfmt.RedirectOverwrite)
	require.NotZero(t, result.ExitCode, diagnostics)
	_, err = os.Stat(filepath.Join(dir, "producer-ran"))
	require.True(t, os.IsNotExist(err))
	result, diagnostics = run("touch producer-ran", "artifact", planfmt.RedirectOverwrite, planfmt.Arg{Key: "atomic", Val: planfmt.Value{Kind: planfmt.ValueBool, Bool: true}})
	require.NotZero(t, result.ExitCode)
	require.Contains(t, diagnostics, "not supported")
	_, err = os.Stat(filepath.Join(dir, "producer-ran"))
	require.True(t, os.IsNotExist(err))
}
