package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSinkQuotedPathsPreserveLiteralBackslashes(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, name := range []string{`name\nfile`, `C:\logs\out`, `a\"quoted file`} {
		t.Run(name, func(t *testing.T) {
			bare, v := planSinkScript(t, fmt.Sprintf(`printf payload > "%s"`, name))
			explicit, _ := planSinkScript(t, fmt.Sprintf(`printf payload > @file("%s")`, name))
			a, err := bare.Canonicalize()
			require.NoError(t, err)
			b, err := explicit.Canonicalize()
			require.NoError(t, err)
			require.Equal(t, b, a, "both spellings must select the same destination")
			var stderr bytes.Buffer
			result, err := ExecutePlan(context.Background(), bare, Config{Stderr: &stderr}, v)
			require.NoError(t, err)
			require.Zero(t, result.ExitCode, stderr.String())
			data, err := os.ReadFile(name)
			require.NoError(t, err)
			require.Equal(t, "payload", string(data))
		})
	}
}

func TestSinkVariableOptionsRetainTheirTypes(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Run("atomic boolean", func(t *testing.T) {
		require.NoError(t, os.WriteFile("artifact", []byte("old"), 0o600))
		p, v := planSinkScript(t, `var atomic = true
sh -c 'printf partial; exit 23' > @file("artifact", atomic=@var.atomic)`)
		var stderr bytes.Buffer
		result, err := ExecutePlan(context.Background(), p, Config{Stderr: &stderr}, v)
		require.NoError(t, err)
		require.Equal(t, 23, result.ExitCode, stderr.String())
		data, err := os.ReadFile("artifact")
		require.NoError(t, err)
		require.Equal(t, "old", string(data), "failed production must not publish")
	})
	t.Run("integer permission", func(t *testing.T) {
		p, v := planSinkScript(t, `var perm = 384
var path = "output-7"
printf payload > @file(@var.path, perm=@var.perm)`)
		var stderr bytes.Buffer
		result, err := ExecutePlan(context.Background(), p, Config{Stderr: &stderr}, v)
		require.NoError(t, err)
		require.Zero(t, result.ExitCode, stderr.String())
		data, err := os.ReadFile("output-7")
		require.NoError(t, err)
		require.Equal(t, "payload", string(data))
		info, err := os.Stat("output-7")
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	})
}

func TestSinkReviewDistinguishesRelativeAndAbsoluteWorkdirs(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	absolute := filepath.Join(base, "absolute")
	relative := absolute[1:]
	_, _ = planSinkScript(t, fmt.Sprintf(`@exec.parallel {
@fs.workdir("%s") { echo relative > out }
@fs.workdir("%s") { echo absolute > out }
}`, relative, absolute))
}
