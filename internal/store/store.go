package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// Meta holds per-slot metadata persisted in meta.json.
type Meta struct {
	AccountUUID string `json:"accountUuid,omitempty"`
	AccountID   string `json:"accountId,omitempty"`
	Email       string `json:"email,omitempty"`
	Plan        string `json:"plan,omitempty"`
	Alias       string `json:"alias,omitempty"`
	Added       int64  `json:"added,omitempty"`
}

// Slot represents a single saved account directory under <root>/<provider>/accounts/<n>.
type Slot struct {
	Number int
	Dir    string
	Meta   Meta
}

// LegacyMigration describes a legacy store folder under $HOME to move into ~/.tori/<Key>.
type LegacyMigration struct {
	Key       string
	LegacyDir string
}

// WriteBytesAtomic atomically writes data to path via a temp file in the same directory.
// If keep is true and path already exists, its existing file permissions are preserved;
// otherwise mode is applied (typically 0600).
func WriteBytesAtomic(path string, data []byte, mode os.FileMode, keep bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if keep {
		if st, err := os.Stat(path); err == nil {
			mode = st.Mode().Perm()
		}
	}
	tmp, err := os.CreateTemp(dir, ".tori-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// WriteJSONAtomic marshals v as indented JSON and writes it atomically.
func WriteJSONAtomic(path string, v any, mode os.FileMode, keep bool) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return WriteBytesAtomic(path, data, mode, keep)
}

// ReadJSON reads and decodes JSON from path into dst. Returns false if missing or invalid.
func ReadJSON(path string, dst any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return false
	}
	return true
}

// ReadJSONMap reads a JSON object from path into a map, returning nil on missing/invalid/non-object.
func ReadJSONMap(path string) map[string]any {
	var m map[string]any
	if !ReadJSON(path, &m) {
		return nil
	}
	return m
}

// AccountsDir returns <root>/<key>/accounts.
func AccountsDir(root, key string) string {
	return filepath.Join(root, key, "accounts")
}

// SlotDir returns <root>/<key>/accounts/<num>.
func SlotDir(root, key string, num int) string {
	return filepath.Join(AccountsDir(root, key), strconv.Itoa(num))
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Slots returns all valid slots for a provider sorted ascending by slot number.
// A slot directory is valid if its name is numeric (> 0) and slotFile exists inside it.
func Slots(root, key, slotFile string) ([]Slot, error) {
	return SlotsAny(root, key, slotFile)
}

// SlotsAny returns all valid slots for a provider sorted ascending by slot number.
// A slot directory is valid if its name is numeric (> 0) and any of slotFiles exists inside it.
func SlotsAny(root, key string, slotFiles ...string) ([]Slot, error) {
	acctDir := AccountsDir(root, key)
	entries, err := os.ReadDir(acctDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Slot
	for _, ent := range entries {
		if !ent.IsDir() || !isAllDigits(ent.Name()) {
			continue
		}
		n, err := strconv.Atoi(ent.Name())
		if err != nil || n <= 0 {
			continue
		}
		dir := filepath.Join(acctDir, ent.Name())
		hasFile := false
		for _, sf := range slotFiles {
			if sf == "" {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, sf)); err == nil {
				hasFile = true
				break
			}
		}
		if !hasFile {
			continue
		}
		var meta Meta
		_ = ReadJSON(filepath.Join(dir, "meta.json"), &meta)
		out = append(out, Slot{Number: n, Dir: dir, Meta: meta})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Number < out[j].Number
	})
	return out, nil
}

// Compact renumbers slots to contiguous 1..N using a two-phase rename so names never collide.
// Returns true if any renumbering occurred.
func Compact(root, key, slotFile string) (bool, error) {
	return CompactAny(root, key, slotFile)
}

// CompactAny renumbers slots matched by any of slotFiles to contiguous 1..N.
func CompactAny(root, key string, slotFiles ...string) (bool, error) {
	slots, err := SlotsAny(root, key, slotFiles...)
	if err != nil {
		return false, err
	}
	alreadyCompact := true
	for i, s := range slots {
		if s.Number != i+1 {
			alreadyCompact = false
			break
		}
	}
	if alreadyCompact {
		return false, nil
	}

	acctDir := AccountsDir(root, key)
	moving := make([]string, 0, len(slots))
	for _, s := range slots {
		tmpDir := filepath.Join(acctDir, fmt.Sprintf(".moving-%d", s.Number))
		if err := os.Rename(s.Dir, tmpDir); err != nil {
			// Best-effort rollback of already-moved dirs in phase 1
			for j, m := range moving {
				_ = os.Rename(m, slots[j].Dir)
			}
			return false, err
		}
		moving = append(moving, tmpDir)
	}
	for i, tmpDir := range moving {
		dstDir := filepath.Join(acctDir, strconv.Itoa(i+1))
		if isEmptyDir(dstDir) {
			_ = os.Remove(dstDir)
		}
		if err := os.Rename(tmpDir, dstDir); err != nil {
			return false, err
		}
	}
	return true, nil
}

// RemoveSlot deletes a slot directory and compacts remaining slots to 1..N.
func RemoveSlot(root, key, slotFile string, num int) (bool, error) {
	dir := SlotDir(root, key, num)
	if err := os.RemoveAll(dir); err != nil {
		return false, err
	}
	return Compact(root, key, slotFile)
}

func isEmptyDir(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) == 0
}

// MigrateLegacy performs a one-time move of legacy directories (~/.claudeswap, ~/.codexswap, ~/.aswap)
// into <root>/<key> when TORI_HOME is not overridden and the destination does not exist or is empty.
func MigrateLegacy(root, home string, toriHomeOverridden bool, migrations []LegacyMigration) (notices []string, warnings []string) {
	if toriHomeOverridden || home == "" {
		return nil, nil
	}
	defaultRoot := filepath.Join(home, ".tori")
	for _, m := range migrations {
		if m.LegacyDir == "" {
			continue
		}
		oldPath := filepath.Join(home, m.LegacyDir)
		info, err := os.Stat(oldPath)
		if err != nil || !info.IsDir() {
			continue
		}
		destPath := filepath.Join(root, m.Key)
		if destInfo, err := os.Stat(destPath); err == nil {
			if !destInfo.IsDir() || !isEmptyDir(destPath) {
				continue
			}
			// Destination exists as an empty dir; remove it so os.Rename succeeds cleanly.
			if err := os.Remove(destPath); err != nil {
				warnings = append(warnings, fmt.Sprintf("could not move ~/%s: %v", m.LegacyDir, err))
				continue
			}
		}
		if err := os.MkdirAll(root, 0o700); err != nil {
			warnings = append(warnings, fmt.Sprintf("could not move ~/%s: %v", m.LegacyDir, err))
			continue
		}
		if err := os.Rename(oldPath, destPath); err != nil {
			warnings = append(warnings, fmt.Sprintf("could not move ~/%s: %v", m.LegacyDir, err))
			continue
		}
		if root == defaultRoot {
			notices = append(notices, fmt.Sprintf("tori: moved saved accounts from ~/%s to ~/.tori/%s", m.LegacyDir, m.Key))
		} else {
			notices = append(notices, fmt.Sprintf("tori: moved saved accounts from ~/%s to %s", m.LegacyDir, destPath))
		}
	}
	return notices, warnings
}
