package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsageDefaultsAndSetUsage(t *testing.T) {
	root := t.TempDir()

	for _, k := range ToolKeys {
		if UsageEnabled(root, k) {
			t.Fatalf("expected usage off by default for %s", k)
		}
	}

	if err := SetUsage(root, []string{"claude"}, true); err != nil {
		t.Fatalf("SetUsage failed: %v", err)
	}
	info, err := os.Stat(Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected config.json mode 0600, got %o", info.Mode().Perm())
	}

	if !UsageEnabled(root, "claude") {
		t.Fatal("expected claude usage enabled")
	}
	if UsageEnabled(root, "codex") || UsageEnabled(root, "antigravity") {
		t.Fatal("expected codex and antigravity usage still off")
	}

	if err := SetUsage(root, []string{"claude", "codex"}, false); err != nil {
		t.Fatal(err)
	}
	if UsageEnabled(root, "claude") {
		t.Fatal("expected claude usage off after toggle")
	}
}

func TestDisclaimerLines(t *testing.T) {
	lines := DisclaimerLines([]string{"claude", "antigravity"})
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, DisclaimerHead) {
		t.Fatal("missing DisclaimerHead")
	}
	if !strings.Contains(joined, DisclaimerTool["claude"]) {
		t.Fatal("missing claude disclaimer")
	}
	if !strings.Contains(joined, DisclaimerTool["antigravity"]) {
		t.Fatal("missing antigravity disclaimer")
	}
	if strings.Contains(joined, DisclaimerTool["codex"]) {
		t.Fatal("unexpected codex disclaimer")
	}
	if !strings.Contains(joined, DisclaimerTail) {
		t.Fatal("missing DisclaimerTail")
	}
	_ = filepath.Base(joined)
}
