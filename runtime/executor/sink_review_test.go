package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/builtwithtofu/sigil/core/planfmt/formatter"
	"github.com/builtwithtofu/sigil/runtime/lexer"
	"github.com/builtwithtofu/sigil/runtime/parser"
	"github.com/builtwithtofu/sigil/runtime/planner"
	"github.com/stretchr/testify/require"
)

func TestSinkReviewRejectsConcurrentIdenticalDestinations(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)
	for _, tc := range []struct {
		name, source string
		collision    bool
	}{
		{"same", "@exec.parallel {\necho a > same\necho b > @file(\"same\")\n}", true},
		{"same variable", "var destination = \"same\"\n@exec.parallel {\necho a > @var.destination\necho b > @file(@var.destination)\n}", true},
		{"distinct", "@exec.parallel {\necho a > first\necho b > second\n}", false},
		{"sequential", "echo a > same\necho b > same", false},
		{"different directories", "@exec.parallel {\n@fs.workdir(\"one\") { echo a > same }\n@fs.workdir(\"two\") { echo b > same }\n}", false},
		{"same absolute despite directories", "@exec.parallel {\n@fs.workdir(\"one\") { echo a > /same }\n@fs.workdir(\"two\") { echo b > /same }\n}", true},
		{"same directory relative and absolute", fmt.Sprintf("@exec.parallel {\n@fs.workdir(\"one\") { echo a > same }\n@fs.workdir(%q) { echo b > same }\n}", filepath.Join(cwd, "one")), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree := parser.Parse([]byte(tc.source))
			require.Empty(t, tree.Errors)
			lex := lexer.NewLexer()
			lex.Init([]byte(tc.source))
			_, err := planner.Plan(tree.Events, lex.GetTokens(), planner.Config{Vault: testVault()})
			if tc.collision {
				require.ErrorContains(t, err, "same declared destination")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSinkReviewWarnsAboutAppendReplay(t *testing.T) {
	p, _ := planSinkScript(t, "@exec.retry(times=2) {\necho payload >> output\n}")
	text := formatter.Format(p)
	require.Contains(t, text, "retrying append can duplicate output")
	require.Contains(t, text, "not exactly-once")
	var tree bytes.Buffer
	formatter.FormatTree(&tree, p, false)
	require.Contains(t, tree.String(), "retrying append can duplicate output")
}

func TestSinkContractTracksDeclarationsNotContents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact")
	source := fmt.Sprintf("printf payload > @file(%q)", path)
	p, _ := planSinkScript(t, source)
	original, err := p.Canonicalize()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("external edit"), 0o600))
	p, _ = planSinkScript(t, source)
	same, err := p.Canonicalize()
	require.NoError(t, err)
	require.Equal(t, original, same)
	for _, source := range []string{
		fmt.Sprintf("printf payload > @file(%q)", path+"-changed"),
		fmt.Sprintf("printf payload > @file(%q, perm=384)", path),
		fmt.Sprintf("printf payload > @file(%q, atomic=true)", path),
		fmt.Sprintf("printf payload >> @file(%q)", path),
	} {
		p, _ := planSinkScript(t, source)
		changed, err := p.Canonicalize()
		require.NoError(t, err)
		require.NotEqual(t, original, changed)
	}
}

func TestSinkInterpolatedQuotedPathUsesReviewedDirectory(t *testing.T) {
	dir := t.TempDir()
	source := fmt.Sprintf("var name = \"a quoted file\"\n@fs.workdir(%q) { printf payload > \"@var.name\" }", dir)
	p, v := planSinkScript(t, source)
	explicit, _ := planSinkScript(t, fmt.Sprintf("var name = \"a quoted file\"\n@fs.workdir(%q) { printf payload > @file(@var.name) }", dir))
	a, err := p.Canonicalize()
	require.NoError(t, err)
	b, err := explicit.Canonicalize()
	require.NoError(t, err)
	require.Equal(t, a, b, "interpolated endpoint spellings must have the same contract")
	var stderr bytes.Buffer
	result, err := ExecutePlan(context.Background(), p, Config{Stderr: &stderr}, v)
	require.NoError(t, err)
	require.Zero(t, result.ExitCode, stderr.String())
	data, err := os.ReadFile(filepath.Join(dir, "a quoted file"))
	require.NoError(t, err)
	require.Equal(t, "payload", string(data))
}

func TestSinkPlanShowsEffectivePolicyAndScope(t *testing.T) {
	p, _ := planSinkScript(t, "echo payload > artifact")
	text := formatter.Format(p)
	require.Contains(t, text, "@file")
	require.Contains(t, text, "atomic=false")
	require.Contains(t, text, "perm=420")
	require.Contains(t, text, "context=")
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.Contains(t, text, cwd)
}

func TestSinkContractPinsRootDirectory(t *testing.T) {
	reviewed, current := t.TempDir(), t.TempDir()
	t.Chdir(reviewed)
	p, v := planSinkScript(t, "printf payload > artifact")
	original, err := p.Canonicalize()
	require.NoError(t, err)
	t.Chdir(current)
	changed, _ := planSinkScript(t, "printf payload > artifact")
	canonical, err := changed.Canonicalize()
	require.NoError(t, err)
	require.NotEqual(t, original, canonical)
	result, err := ExecutePlan(context.Background(), p, Config{}, v)
	require.NoError(t, err)
	require.Zero(t, result.ExitCode)
	data, err := os.ReadFile(filepath.Join(reviewed, "artifact"))
	require.NoError(t, err)
	require.Equal(t, "payload", string(data))
	_, err = os.Stat(filepath.Join(current, "artifact"))
	require.True(t, os.IsNotExist(err))
}

func TestRetriedAppendCanDuplicateOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events")
	source := fmt.Sprintf("@exec.retry(delay=0s, times=2) { sh -c 'printf entry; exit 23' >> %q }", path)
	result, diagnostics := runFaultyOutput(t, context.Background(), source, &outputFault{})
	require.Equal(t, 23, result.ExitCode, diagnostics)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "entryentry", string(data))
}
