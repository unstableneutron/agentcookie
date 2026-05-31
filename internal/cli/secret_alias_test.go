package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSecrets(t *testing.T, home, cli, content string) {
	t.Helper()
	dir := filepath.Join(home, ".agentcookie", "secrets", cli)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func envLines(t *testing.T, cli string) string {
	t.Helper()
	buf := &bytes.Buffer{}
	cmd := secretEnvCmd
	cmd.SetOut(buf)
	if err := runSecretEnv(cmd, []string{cli}); err != nil {
		t.Fatalf("runSecretEnv: %v", err)
	}
	return buf.String()
}

func TestSecretEnv_AppliesAlias(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSecrets(t, home, "tesla-pp-cli", "OAUTH_BEARER=tok-bearer\nOAUTH_REFRESH=tok-refresh\n")
	// Map the consumer's declared name to the synced bearer key.
	aliasCmd := secretAliasCmd
	aliasCmd.SetOut(&bytes.Buffer{})
	if err := runSecretAlias(aliasCmd, []string{"tesla-pp-cli", "TESLA_AUTH_TOKEN", "OAUTH_BEARER"}); err != nil {
		t.Fatalf("set alias: %v", err)
	}

	out := envLines(t, "tesla-pp-cli")
	if !strings.Contains(out, "TESLA_AUTH_TOKEN=tok-bearer") {
		t.Errorf("expected TESLA_AUTH_TOKEN mapped to the bearer value, got:\n%s", out)
	}
	if strings.Contains(out, "TESLA_AUTH_TOKEN=tok-refresh") {
		t.Errorf("alias must map to OAUTH_BEARER, not the refresh token:\n%s", out)
	}
	// Stored keys still emitted under their own names.
	if !strings.Contains(out, "OAUTH_BEARER=tok-bearer") {
		t.Errorf("stored keys should still be emitted:\n%s", out)
	}
}

func TestSecretEnv_NoAliasesUnchanged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSecrets(t, home, "demo-cli", "K1=v1\nK2=v2\n")
	out := envLines(t, "demo-cli")
	if out != "K1=v1\nK2=v2\n" {
		t.Errorf("no-alias output should be unchanged, got:\n%q", out)
	}
}

func TestSecretEnv_AliasToMissingKeyNoOp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSecrets(t, home, "demo-cli", "OAUTH_BEARER=tok\n")
	aliasCmd := secretAliasCmd
	aliasCmd.SetOut(&bytes.Buffer{})
	if err := runSecretAlias(aliasCmd, []string{"demo-cli", "DECLARED", "NONEXISTENT_KEY"}); err != nil {
		t.Fatalf("set alias: %v", err)
	}
	out := envLines(t, "demo-cli")
	if strings.Contains(out, "DECLARED=") {
		t.Errorf("alias to a missing stored key must emit nothing for it, got:\n%s", out)
	}
	if !strings.Contains(out, "OAUTH_BEARER=tok") {
		t.Errorf("stored key still expected:\n%s", out)
	}
}

func TestSecretAlias_SetAndList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	set := secretAliasCmd
	set.SetOut(&bytes.Buffer{})
	if err := runSecretAlias(set, []string{"demo-cli", "TESLA_AUTH_TOKEN", "OAUTH_BEARER"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".agentcookie", "secrets", "demo-cli", "aliases.env")); err != nil {
		t.Fatalf("aliases.env not written: %v", err)
	}
	buf := &bytes.Buffer{}
	list := secretAliasCmd
	list.SetOut(buf)
	if err := runSecretAlias(list, []string{"demo-cli"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(buf.String(), "TESLA_AUTH_TOKEN <- OAUTH_BEARER") {
		t.Errorf("list output missing alias: %q", buf.String())
	}
}

func TestSecretAlias_InvalidName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cmd := secretAliasCmd
	cmd.SetOut(&bytes.Buffer{})
	if err := runSecretAlias(cmd, []string{"demo-cli", "bad name!", "OAUTH_BEARER"}); err == nil {
		t.Error("expected error for invalid declared env var name")
	}
}
