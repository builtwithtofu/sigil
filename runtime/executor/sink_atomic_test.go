package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/builtwithtofu/sigil/core/planfmt"
	"github.com/stretchr/testify/require"
)

func TestAtomicFileRedirectPublishesOnlySuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o600))
	atomic := planfmt.Arg{Key: "atomic", Val: planfmt.Value{Kind: planfmt.ValueBool, Bool: true}}
	result, diagnostics := runFileRedirect(t, "printf partial; exit 23", path, planfmt.RedirectOverwrite, atomic)
	require.Equal(t, 23, result.ExitCode, diagnostics)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "old", string(data))
	result, diagnostics = runFileRedirect(t, "printf complete", path, planfmt.RedirectOverwrite, atomic)
	require.Zero(t, result.ExitCode, diagnostics)
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "complete", string(data))
	result, diagnostics = runFileRedirect(t, "true", path, planfmt.RedirectOverwrite, atomic)
	require.Zero(t, result.ExitCode, diagnostics)
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Empty(t, data)
	files, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, files, 1, "staging was left behind")
}

func TestAtomicRetryStartsWithFreshStaging(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact")
	marker := filepath.Join(dir, "attempted")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o600))
	command := fmt.Sprintf("if test -f %q; then test \"$(cat %q)\" = old || exit 99; printf complete; else touch %q; printf failed; exit 23; fi", marker, path, marker)
	source := fmt.Sprintf("@exec.retry(delay=0s, times=2) { sh -c '%s' > @file(%q, atomic=true) }", command, path)
	fault := &outputFault{}
	result, diagnostics := runFaultyOutput(t, context.Background(), source, fault)
	require.Zero(t, result.ExitCode, diagnostics)
	require.EqualValues(t, 2, fault.opens.Load())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "complete", string(data))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 2)
}

func TestAtomicUnsupportedTargetsDoNotRunProducer(t *testing.T) {
	for _, kind := range []string{"append", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "artifact")
			marker := filepath.Join(dir, "producer-ran")
			mode := planfmt.RedirectOverwrite
			switch kind {
			case "append":
				mode = planfmt.RedirectAppend
			case "symlink":
				require.NoError(t, os.Symlink(filepath.Join(dir, "elsewhere"), path))
			case "directory":
				require.NoError(t, os.Mkdir(path, 0o700))
			}
			result, diagnostics := runFileRedirect(t, fmt.Sprintf("touch %q", marker), path, mode, planfmt.Arg{Key: "atomic", Val: planfmt.Value{Kind: planfmt.ValueBool, Bool: true}})
			require.NotZero(t, result.ExitCode, diagnostics)
			_, err := os.Stat(marker)
			require.True(t, os.IsNotExist(err))
		})
	}
}

func TestAtomicPublicationFailureIsReportedAndStagingRemoved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact")
	// The producer creates an incompatible destination after staging opens.
	result, diagnostics := runFileRedirect(t, fmt.Sprintf("mkdir %q; printf payload", path), path, planfmt.RedirectOverwrite, planfmt.Arg{Key: "atomic", Val: planfmt.Value{Kind: planfmt.ValueBool, Bool: true}})
	require.NotZero(t, result.ExitCode)
	require.Contains(t, diagnostics, "finish failed")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.True(t, entries[0].IsDir())
}
