package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/builtwithtofu/sigil/core/planfmt"
	"github.com/builtwithtofu/sigil/runtime/lexer"
	"github.com/builtwithtofu/sigil/runtime/parser"
	"github.com/builtwithtofu/sigil/runtime/planner"
	"github.com/builtwithtofu/sigil/runtime/vault"
	"github.com/stretchr/testify/require"
)

func planSinkScript(t *testing.T, source string) (*planfmt.Plan, *vault.Vault) {
	t.Helper()
	tree := parser.Parse([]byte(source))
	require.Empty(t, tree.Errors)
	lex := lexer.NewLexer()
	lex.Init([]byte(source))
	v := testVault()
	p, err := planner.Plan(tree.Events, lex.GetTokens(), planner.Config{Vault: v})
	require.NoError(t, err)
	return p, v
}

func TestSinkContractBareAndExplicitPathsAgree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a quoted file")
	bare, v := planSinkScript(t, fmt.Sprintf("printf payload > %q", path))
	explicit, _ := planSinkScript(t, fmt.Sprintf("printf payload > @file(%q)", path))
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "planning wrote a file")
	a, err := bare.Canonicalize()
	require.NoError(t, err)
	b, err := explicit.Canonicalize()
	require.NoError(t, err)
	require.Equal(t, a, b, "equivalent endpoint syntax must produce the same contract")
	var wire bytes.Buffer
	_, err = planfmt.Write(&wire, bare)
	require.NoError(t, err)
	restored, _, err := planfmt.Read(&wire)
	require.NoError(t, err)
	result, err := ExecutePlan(context.Background(), restored, Config{}, v)
	require.NoError(t, err)
	require.Zero(t, result.ExitCode)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "payload", string(data))
}
