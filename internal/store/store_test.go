package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteBytesAtomicPermissionsAndKeep(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "secret.json")

	if err := WriteBytesAtomic(p, []byte(`{"a":1}`), 0o600, false); err != nil {
		t.Fatalf("WriteBytesAtomic failed: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected perm 0600, got %o", perm)
	}

	// Verify keep=true preserves existing permissions.
	p2 := filepath.Join(dir, "keep.json")
	if err := os.WriteFile(p2, []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteBytesAtomic(p2, []byte(`{"x":2}`), 0o600, true); err != nil {
		t.Fatalf("WriteBytesAtomic keep=true failed: %v", err)
	}
	info2, err := os.Stat(p2)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info2.Mode().Perm(); perm != 0o644 {
		t.Fatalf("expected preserved perm 0644, got %o", perm)
	}

	// Ensure no leftover temp files.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 files, got %d", len(entries))
	}
}

func TestCompactTwoPhaseRebalancing(t *testing.T) {
	root := t.TempDir()
	key := "claude"
	slotFile := "oauth.json"

	// Create slots 2, 4, 5 (and an invalid slot 3 missing slotFile, and a non-numeric dir).
	for _, n := range []string{"2", "4", "5"} {
		d := filepath.Join(root, key, "accounts", n)
		if err := WriteJSONAtomic(filepath.Join(d, slotFile), map[string]any{"slot": n}, 0o600, false); err != nil {
			t.Fatal(err)
		}
		if err := WriteJSONAtomic(filepath.Join(d, "meta.json"), Meta{Email: "user" + n + "@example.com"}, 0o600, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, key, "accounts", "3"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, key, "accounts", "notes"), 0o700); err != nil {
		t.Fatal(err)
	}

	slots, err := Slots(root, key, slotFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 3 || slots[0].Number != 2 || slots[1].Number != 4 || slots[2].Number != 5 {
		t.Fatalf("unexpected initial slots: %+v", slots)
	}

	changed, err := Compact(root, key, slotFile)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}
	if !changed {
		t.Fatal("expected Compact to report changes")
	}

	slots, err = Slots(root, key, slotFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 3 {
		t.Fatalf("expected 3 slots, got %d", len(slots))
	}
	wantEmails := []string{"user2@example.com", "user4@example.com", "user5@example.com"}
	for i, s := range slots {
		if s.Number != i+1 {
			t.Fatalf("expected slot %d, got %d", i+1, s.Number)
		}
		if s.Meta.Email != wantEmails[i] {
			t.Fatalf("slot %d email = %q, want %q", s.Number, s.Meta.Email, wantEmails[i])
		}
	}

	// Second compact should be a no-op.
	changed, err = Compact(root, key, slotFile)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected second Compact to be a no-op")
	}
}

func TestRemoveSlotCompacts(t *testing.T) {
	root := t.TempDir()
	key := "codex"
	slotFile := "auth.json"
	for _, n := range []string{"1", "2", "3"} {
		d := filepath.Join(root, key, "accounts", n)
		if err := WriteJSONAtomic(filepath.Join(d, slotFile), map[string]any{"n": n}, 0o600, false); err != nil {
			t.Fatal(err)
		}
		if err := WriteJSONAtomic(filepath.Join(d, "meta.json"), Meta{Email: "u" + n + "@example.com"}, 0o600, false); err != nil {
			t.Fatal(err)
		}
	}
	renum, err := RemoveSlot(root, key, slotFile, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !renum {
		t.Fatal("expected renumbering after removing slot 1")
	}
	slots, err := Slots(root, key, slotFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 2 || slots[0].Number != 1 || slots[0].Meta.Email != "u2@example.com" || slots[1].Number != 2 || slots[1].Meta.Email != "u3@example.com" {
		t.Fatalf("unexpected slots after remove: %+v", slots)
	}
}

func TestMigrateLegacy(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".tori")
	migrations := []LegacyMigration{
		{Key: "claude", LegacyDir: ".claudeswap"},
		{Key: "antigravity", LegacyDir: ".aswap"},
	}

	// Populate legacy folders.
	if err := WriteJSONAtomic(filepath.Join(home, ".claudeswap", "accounts", "1", "oauth.json"), map[string]any{"ok": true}, 0o600, false); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSONAtomic(filepath.Join(home, ".aswap", "accounts", "1", "acp_token.json"), map[string]any{"ok": true}, 0o600, false); err != nil {
		t.Fatal(err)
	}

	// Skipped when TORI_HOME is overridden.
	notices, warnings := MigrateLegacy(filepath.Join(home, "custom"), home, true, migrations)
	if len(notices) != 0 || len(warnings) != 0 {
		t.Fatalf("expected no migration when TORI_HOME overridden, got notices=%v warnings=%v", notices, warnings)
	}
	if _, err := os.Stat(filepath.Join(home, ".claudeswap")); err != nil {
		t.Fatal("expected .claudeswap untouched when overridden")
	}

	// Also test when empty destination exists for antigravity.
	if err := os.MkdirAll(filepath.Join(root, "antigravity"), 0o700); err != nil {
		t.Fatal(err)
	}

	notices, warnings = MigrateLegacy(root, home, false, migrations)
	if len(warnings) != 0 {
		t.Fatalf("unexpected migration warnings: %v", warnings)
	}
	if len(notices) != 2 {
		t.Fatalf("expected 2 migration notices, got %v", notices)
	}
	if _, err := os.Stat(filepath.Join(root, "claude", "accounts", "1", "oauth.json")); err != nil {
		t.Fatalf("expected migrated claude oauth.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "antigravity", "accounts", "1", "acp_token.json")); err != nil {
		t.Fatalf("expected migrated antigravity acp_token.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claudeswap")); !os.IsNotExist(err) {
		t.Fatal("expected old .claudeswap to be moved")
	}
}
