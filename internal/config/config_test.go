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

	for _, k := range []string{"claude", "codex"} {
		if !UsageEnabled(root, k) {
			t.Fatalf("expected usage on by default for %s", k)
		}
	}
	if UsageEnabled(root, "antigravity") {
		t.Fatal("expected antigravity usage off by default")
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
	if !UsageEnabled(root, "codex") {
		t.Fatal("expected codex usage still on")
	}
	if UsageEnabled(root, "antigravity") {
		t.Fatal("changing another tool must not turn on antigravity usage")
	}

	if err := SetUsage(root, []string{"claude"}, true); err != nil {
		t.Fatal(err)
	}
	if !UsageEnabled(root, "claude") {
		t.Fatal("expected claude usage on after re-enabling")
	}
}

func TestAntigravityUsageNeedsExplicitOptIn(t *testing.T) {
	root := t.TempDir()

	// Earlier releases wrote usage.antigravity = true without asking.
	if err := os.WriteFile(Path(root), []byte(`{"usage":{"antigravity":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if UsageEnabled(root, "antigravity") {
		t.Fatal("expected legacy antigravity usage=true to be ignored")
	}

	if err := SetUsage(root, []string{"antigravity"}, true); err != nil {
		t.Fatal(err)
	}
	if !UsageEnabled(root, "antigravity") {
		t.Fatal("expected antigravity usage on after opting in")
	}
	if err := SetUsage(root, []string{"claude"}, false); err != nil {
		t.Fatal(err)
	}
	if !UsageEnabled(root, "antigravity") {
		t.Fatal("changing another tool must keep the antigravity opt-in")
	}

	if err := SetUsage(root, []string{"antigravity"}, false); err != nil {
		t.Fatal(err)
	}
	if UsageEnabled(root, "antigravity") {
		t.Fatal("expected antigravity usage off after turning it off")
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
