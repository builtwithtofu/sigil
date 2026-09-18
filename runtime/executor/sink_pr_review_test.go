package executor

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/builtwithtofu/sigil/core/decorator"
	"github.com/builtwithtofu/sigil/core/planfmt"
	"github.com/stretchr/testify/require"
)

func TestExecutePlanRejectsConflictingOutputsBeforeStarting(t *testing.T) {
	t.Chdir(t.TempDir())
	p, v := planSinkScript(t, "@exec.parallel {\necho first > first\necho second > second\n}")
	branches := p.Steps[0].Tree.(*planfmt.CommandNode).Block
	first := branches[0].Tree.(*planfmt.RedirectNode)
	second := branches[1].Tree.(*planfmt.RedirectNode)
	second.Target = first.Target
	var wire bytes.Buffer
	_, err := planfmt.Write(&wire, p)
	require.NoError(t, err)
	p, _, err = planfmt.Read(&wire)
	require.NoError(t, err)
	_, err = ExecutePlan(context.Background(), p, Config{}, v)
	require.ErrorContains(t, err, "same declared destination")
}

func TestCompositeArgumentsResolveAuthorizedValues(t *testing.T) {
	v := testVault()
	id := v.DeclareVariableTransportSensitive("TOKEN", "@env.TOKEN")
	v.MarkTouched(id)
	v.StoreUnresolvedValue(id, "credential")
	v.ResolveAllTouched()
	display := v.GetDisplayID(id)
	params := map[string]any{"options": map[string]any{"headers": []any{display, "Bearer " + display}}}
	e := &executor{vault: v}
	resolved, err := e.resolveDisplayIDs(params, "@file", "local")
	require.NoError(t, err)
	require.Equal(t, map[string]any{"options": map[string]any{"headers": []any{"credential", "Bearer credential"}}}, resolved)
	require.Equal(t, []any{display, "Bearer " + display}, params["options"].(map[string]any)["headers"], "resolution must not mutate the plan")
	_, err = e.resolveDisplayIDs(params, "@file", "other-transport")
	require.Error(t, err, "nested references must retain transport authorization")
}

func TestIsolatedFileOutputRetainsSessionDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	session, err := openIsolatedSession(map[string]any{"level": "none"})
	require.NoError(t, err)
	defer session.Close()
	output, err := decorator.OpenFileOutput(context.Background(), session, "output", decorator.FileTruncate, 0o600)
	require.NoError(t, err)
	defer output.Abort(context.Background())
	_, err = output.Write([]byte("payload"))
	require.NoError(t, err)
	require.NoError(t, output.Finish(context.Background()))
	data, err := os.ReadFile("output")
	require.NoError(t, err)
	require.Equal(t, "payload", string(data))
}
