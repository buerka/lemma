package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/buerka/lemma/internal/schema"
)

// ComputeChangesetHash hashes a changeset with its hash field cleared.
func ComputeChangesetHash(cs schema.Changeset) (string, error) {
	cs.Hash = ""
	return HashValue(cs)
}

func LoadChangeset(path string) (*schema.Changeset, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, Errorf(CodeNotFound, "changeset not found: %s", path)
		}
		return nil, Wrap(CodeGeneral, "read changeset", err)
	}
	var cs schema.Changeset
	if err := json.Unmarshal(raw, &cs); err != nil {
		return nil, Wrap(CodeGeneral, "parse changeset", err)
	}
	if err := cs.Validate(); err != nil {
		return nil, Wrap(CodeGeneral, "invalid changeset", err)
	}
	return &cs, nil
}

func SaveChangeset(path string, cs *schema.Changeset) error {
	if err := cs.Validate(); err != nil {
		return Wrap(CodeGeneral, "refusing to write invalid changeset", err)
	}
	b, err := MarshalIndentNoEscape(cs)
	if err != nil {
		return Wrap(CodeGeneral, "encode changeset", err)
	}
	if err := AtomicWrite(path, b, 0o644); err != nil {
		return Wrap(CodeGeneral, "write changeset", err)
	}
	return nil
}

// VerifyChangesetFile recomputes the changeset hash.
func VerifyChangesetFile(path string) (*schema.Changeset, bool, error) {
	cs, err := LoadChangeset(path)
	if err != nil {
		return nil, false, err
	}
	got, err := ComputeChangesetHash(*cs)
	if err != nil {
		return nil, false, err
	}
	return cs, got == cs.Hash, nil
}

// FindChangeset resolves a path, a path without .json, or a changeset id.
func FindChangeset(ledgerDir, ref string) (string, *schema.Changeset, error) {
	if strings.TrimSpace(ref) == "" {
		return "", nil, Errorf(CodeUsage, "changeset reference is required")
	}
	candidates := []string{ref}
	if filepath.Ext(ref) == "" {
		candidates = append(candidates, ref+".json")
	}
	changesetsDir := filepath.Join(ledgerDir, "changesets")
	for _, c := range candidates {
		if !filepath.IsAbs(c) {
			candidates = append(candidates, filepath.Join(changesetsDir, c))
		}
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			cs, err := LoadChangeset(c)
			if err != nil {
				return "", nil, err
			}
			return filepath.Clean(c), cs, nil
		}
	}

	// Last resort: search by changeset_id inside changesets/.
	entries, err := os.ReadDir(changesetsDir)
	if err != nil {
		return "", nil, Errorf(CodeNotFound, "changeset %q not found", ref)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(changesetsDir, e.Name())
		cs, err := LoadChangeset(p)
		if err != nil {
			continue
		}
		if cs.ChangesetID == ref {
			return p, cs, nil
		}
	}
	return "", nil, Errorf(CodeNotFound, "changeset %q not found", ref)
}

type ChangesetInfo struct {
	Path       string            `json:"path"`
	Changeset  *schema.Changeset `json:"changeset"`
	HashValid  bool              `json:"hash_valid"`
	Applied    bool              `json:"applied"`
	GateExists bool              `json:"gate_exists"`
}

func ListChangesets(l *Ledger) ([]ChangesetInfo, error) {
	entries, err := os.ReadDir(l.ChangesetsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, Wrap(CodeGeneral, "read changesets directory", err)
	}
	gates, err := l.ReadGates()
	if err != nil {
		return nil, err
	}
	var out []ChangesetInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(l.ChangesetsDir(), e.Name())
		cs, err := LoadChangeset(p)
		if err != nil {
			continue
		}
		got, err := ComputeChangesetHash(*cs)
		if err != nil {
			continue
		}
		receipt, err := l.LoadReceipt(cs.ChangesetID)
		if err != nil {
			continue
		}
		out = append(out, ChangesetInfo{
			Path:       p,
			Changeset:  cs,
			HashValid:  got == cs.Hash,
			Applied:    receipt != nil && receipt.ChangesetHash == cs.Hash,
			GateExists: GateForHash(gates, cs.ChangesetID, cs.Hash) != nil,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Changeset.CreatedAt < out[j].Changeset.CreatedAt
	})
	return out, nil
}

var changesetNameRe = regexp.MustCompile(`^cs-(\d{8})-(\d{3,})\.json$`)

// NextChangesetID returns cs-YYYYMMDD-NNN, one greater than existing ids for
// the same date.
func NextChangesetID(l *Ledger, date string) (string, error) {
	date = strings.ReplaceAll(date, "-", "")
	max := 0
	entries, err := os.ReadDir(l.ChangesetsDir())
	if err != nil && !os.IsNotExist(err) {
		return "", Wrap(CodeGeneral, "read changesets directory", err)
	}
	for _, e := range entries {
		m := changesetNameRe.FindStringSubmatch(e.Name())
		if m == nil || m[1] != date {
			continue
		}
		n, err := strconv.Atoi(m[2])
		if err == nil && n > max {
			max = n
		}
	}
	return fmt.Sprintf("cs-%s-%03d", date, max+1), nil
}

func ChangesetIDFromPath(path string) (string, bool) {
	base := filepath.Base(path)
	if !strings.HasSuffix(base, ".json") {
		return "", false
	}
	id := strings.TrimSuffix(base, ".json")
	if !schema.ValidChangesetID(id) {
		return "", false
	}
	return id, true
}

// EvidenceHash resolves evidence relative to the project root and returns its
// SHA-256. Paths beginning with progress/ are also accepted.
// NormalizeEvidence stores paths relative to the project root when possible,
// so changesets do not leak machine-specific absolute paths.
func (l *Ledger) NormalizeEvidence(p string) string {
	candidates := []string{ResolvePath(l.Root, p), ResolvePath(l.Dir, p)}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil && WithinDir(l.Root, c) {
			return filepath.ToSlash(l.Rel(c))
		}
	}
	return p
}

func (l *Ledger) EvidenceHash(p string) (string, bool) {
	candidates := []string{ResolvePath(l.Root, p), ResolvePath(l.Dir, p)}
	for _, c := range candidates {
		b, err := os.ReadFile(c)
		if err == nil {
			return HashFileBytes(b), true
		}
	}
	return "", false
}

func IsNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }
