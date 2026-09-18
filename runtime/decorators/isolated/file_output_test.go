package isolated

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/builtwithtofu/sigil/core/decorator"
	"github.com/stretchr/testify/require"
)

func TestFileOutputThroughIsolationWrappers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wrap    func(decorator.Session) decorator.Session
		allowed bool
	}{
		{"network", func(s decorator.Session) decorator.Session { return &networkLoopbackSession{parent: s} }, true},
		{"memory", func(s decorator.Session) decorator.Session { return &memoryLockSession{parent: s} }, true},
		{"readonly", func(s decorator.Session) decorator.Session { return &filesystemReadonlySession{parent: s} }, false},
		{"ephemeral", func(s decorator.Session) decorator.Session { return &filesystemEphemeralSession{parent: s} }, false},
		{"privileges", func(s decorator.Session) decorator.Session { return &privilegesDropSession{parent: s} }, false},
		{"network over readonly", func(s decorator.Session) decorator.Session {
			return &networkLoopbackSession{parent: &filesystemReadonlySession{parent: s}}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			session := tc.wrap(decorator.NewLocalSession()).WithWorkdir(dir).WithEnv(map[string]string{"TEST": "value"})
			output, err := decorator.OpenFileOutput(context.Background(), session, "out", decorator.FileTruncate, 0o600)
			if !tc.allowed {
				require.Error(t, err, "must not bypass filesystem or privilege policy")
				_, err := os.Stat(filepath.Join(dir, "out"))
				require.True(t, os.IsNotExist(err))
				return
			}
			require.NoError(t, err)
			defer output.Abort(context.Background())
			_, err = output.Write([]byte("payload"))
			require.NoError(t, err)
			require.NoError(t, output.Finish(context.Background()))
			data, err := os.ReadFile(filepath.Join(dir, "out"))
			require.NoError(t, err)
			require.Equal(t, "payload", string(data))
		})
	}
}
