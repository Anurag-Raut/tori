package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUsageDefaultsAndSetUsage(t *testing.T) {
	root := t.TempDir()

	for _, k := range ToolKeys {
		if !UsageEnabled(root, k) {
			t.Fatalf("expected usage on by default for %s", k)
		}
	}

	if err := SetUsage(root, []string{"claude"}, false); err != nil {
		t.Fatalf("SetUsage failed: %v", err)
	}
	info, err := os.Stat(Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("expected config.json mode 0600, got %o", info.Mode().Perm())
	}

	if UsageEnabled(root, "claude") {
		t.Fatal("expected claude usage disabled")
	}
	if !UsageEnabled(root, "codex") || !UsageEnabled(root, "antigravity") {
		t.Fatal("expected codex and antigravity usage still on")
	}

	if err := SetUsage(root, []string{"claude"}, true); err != nil {
		t.Fatal(err)
	}
	if !UsageEnabled(root, "claude") {
		t.Fatal("expected claude usage on after re-enabling")
	}
}

func TestDisclaimerLines(t *testing.T) {
	// Claude-only stays short with no provider-specific warning.
	claudeLines := DisclaimerLines([]string{"claude"})
	if len(claudeLines) != 2 {
		t.Fatalf("expected 2 lines for claude, got %d: %v", len(claudeLines), claudeLines)
	}
	if claudeLines[0] != DisclaimerHead || claudeLines[1] != DisclaimerTail {
		t.Fatalf("unexpected claude disclaimer lines: %v", claudeLines)
	}

	// Antigravity includes Google's terms warning.
	lines := DisclaimerLines([]string{"claude", "antigravity"})
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, DisclaimerHead) {
		t.Fatal("missing DisclaimerHead")
	}
	if !strings.Contains(joined, DisclaimerTool["antigravity"]) {
		t.Fatal("missing antigravity disclaimer")
	}
	if !strings.Contains(joined, DisclaimerTail) {
		t.Fatal("missing DisclaimerTail")
	}
	_ = filepath.Base(joined)
}
