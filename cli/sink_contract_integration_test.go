package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSinkContractAndScrubbingBoundaries(t *testing.T) {
	binary := buildOpalBinary(t)
	defer os.Remove(binary)
	dir := t.TempDir()
	artifact := filepath.Join(dir, "private-artifact")
	script := filepath.Join(dir, "sink.sgl")
	contract := filepath.Join(dir, "sink.plan")
	const payload = "private-payload-7391"
	source := fmt.Sprintf("var destination = %q\nvar payload = %q\nfun write { printf \"@var.payload\" > \"@var.destination\" }\n", artifact, payload)
	require.NoError(t, os.WriteFile(script, []byte(source), 0o600))
	display, err := exec.Command(binary, "-f", script, "write", "--dry-run", "--no-color").CombinedOutput()
	require.NoError(t, err, string(display))
	require.NotContains(t, string(display), "private-artifact")
	require.NotContains(t, string(display), payload)
	_, err = os.Stat(artifact)
	require.True(t, os.IsNotExist(err))
	wire, err := exec.Command(binary, "-f", script, "write", "--dry-run", "--resolve").Output()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(contract, wire, 0o600))
	output, err := exec.Command(binary, "--plan", contract, "-f", script).CombinedOutput()
	require.NoError(t, err, string(output))
	data, err := os.ReadFile(artifact)
	require.NoError(t, err)
	require.Equal(t, payload, string(data), "payload bytes must not be scrubbed")
	require.NoError(t, os.WriteFile(artifact, []byte("external edit"), 0o600))
	output, err = exec.Command(binary, "--plan", contract, "-f", script).CombinedOutput()
	require.NoError(t, err, string(output), "destination contents are not contracted")
	require.NoError(t, os.WriteFile(script, []byte(fmt.Sprintf("var destination = %q\nvar payload = %q\nfun write { printf \"@var.payload\" > @file(@var.destination, atomic=true) }\n", artifact, payload)), 0o600))
	output, err = exec.Command(binary, "--plan", contract, "-f", script).CombinedOutput()
	require.Error(t, err, "changed publication policy must invalidate the contract")
	require.NotContains(t, string(output), payload)
	require.NoError(t, os.WriteFile(script, []byte(fmt.Sprintf("var destination = %q\nfun fail { printf payload > \"@var.destination\" }\n", artifact+"/missing")), 0o600))
	output, err = exec.Command(binary, "-f", script, "fail").CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(output), "sigil:")
	require.NotContains(t, string(output), "private-artifact")
}
