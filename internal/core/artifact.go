package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/buerka/lemma/internal/schema"
)

type ArtifactEntry struct {
	RunID      string `json:"run_id"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Path       string `json:"path,omitempty"`
	URI        string `json:"uri,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	Size       int64  `json:"size,omitempty"`
	Local      bool   `json:"local"`
	Verified   bool   `json:"verified,omitempty"`
	VerifiedAt string `json:"verified_at,omitempty"`
	Host       string `json:"host,omitempty"`
}

type ArtifactCheck struct {
	RunID      string `json:"run_id"`
	Kind       string `json:"kind"`
	Path       string `json:"path"`
	SHA256     string `json:"sha256,omitempty"`
	Status     string `json:"status"` // ok | missing | mismatch | remote-verified | remote-unverified | unreadable
	Detail     string `json:"detail,omitempty"`
	VerifiedAt string `json:"verified_at,omitempty"`
	Host       string `json:"host,omitempty"`
}

type ArtifactReport struct {
	OK       bool            `json:"ok"`
	RunID    string          `json:"run_id,omitempty"`
	Strict   bool            `json:"strict"`
	Checks   []ArtifactCheck `json:"checks"`
	Checked  int             `json:"checked"`
	Failed   int             `json:"failed"`
	Warnings int             `json:"warnings"`
}

func artifactRunIDs(l *Ledger, runID string) ([]string, error) {
	if runID != "" {
		if _, err := l.LoadRunSpec(runID); err != nil {
			return nil, err
		}
		return []string{runID}, nil
	}
	runs, err := ListRuns(l)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(runs))
	for _, r := range runs {
		ids = append(ids, r.RunID)
	}
	return ids, nil
}

func ListArtifacts(l *Ledger, runID string) ([]ArtifactEntry, error) {
	ids, err := artifactRunIDs(l, runID)
	if err != nil {
		return nil, err
	}
	out := []ArtifactEntry{}
	for _, id := range ids {
		m, err := LoadManifest(l, id)
		if err != nil {
			continue
		}
		runDir := l.RunDir(id)
		for _, f := range m.Files {
			p := filepath.Join(runDir, filepath.FromSlash(f.Path))
			_, statErr := os.Stat(p)
			out = append(out, ArtifactEntry{
				RunID: id, Kind: "file", Name: filepath.Base(f.Path), Path: f.Path,
				SHA256: f.SHA256, Size: f.Size, Local: statErr == nil,
			})
		}
		for _, a := range m.Artifacts {
			out = append(out, ArtifactEntry{
				RunID: id, Kind: "artifact", Name: a.Name, URI: a.URI,
				SHA256: a.SHA256, Size: a.Size, Verified: a.Verified,
				VerifiedAt: a.VerifiedAt, Host: a.Host,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RunID != out[j].RunID {
			return out[i].RunID < out[j].RunID
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// VerifyArtifacts checks local manifest files against their recorded hashes.
// Remote artifacts are reported as remote-verified only when the runner
// recorded a verified hash; remote-unverified is a warning by default and a
// failure in strict mode.
func VerifyArtifacts(l *Ledger, runID string, strict bool, lockTimeout time.Duration) (*ArtifactReport, error) {
	lock, err := AcquireLock(l.Dir, lockTimeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Release() }()

	ids, err := artifactRunIDs(l, runID)
	if err != nil {
		return nil, err
	}
	report := &ArtifactReport{OK: true, RunID: runID, Strict: strict, Checks: []ArtifactCheck{}}
	for _, id := range ids {
		m, err := LoadManifest(l, id)
		if err != nil {
			report.Checks = append(report.Checks, ArtifactCheck{RunID: id, Kind: "manifest", Path: "manifest.json", Status: "missing", Detail: err.Error()})
			report.Failed++
			continue
		}
		runDir := l.RunDir(id)
		for _, f := range m.Files {
			p := filepath.Join(runDir, filepath.FromSlash(f.Path))
			check := ArtifactCheck{RunID: id, Kind: "file", Path: f.Path, SHA256: f.SHA256}
			b, err := os.ReadFile(p)
			if err != nil {
				check.Status = "missing"
				check.Detail = err.Error()
				report.Failed++
			} else if HashFileBytes(b) != f.SHA256 {
				check.Status = "mismatch"
				check.Detail = fmt.Sprintf("size %d, expected hash %s", len(b), f.SHA256)
				report.Failed++
			} else {
				check.Status = "ok"
			}
			report.Checked++
			report.Checks = append(report.Checks, check)
		}
		for _, a := range m.Artifacts {
			check := ArtifactCheck{
				RunID: id, Kind: "artifact", Path: a.URI, SHA256: a.SHA256,
				VerifiedAt: a.VerifiedAt, Host: a.Host,
			}
			if strings.HasPrefix(a.URI, "file://") {
				p := strings.TrimPrefix(a.URI, "file://")
				b, err := os.ReadFile(p)
				if err != nil {
					check.Status = "missing"
					check.Detail = err.Error()
					report.Failed++
				} else if a.SHA256 != "" && HashFileBytes(b) != a.SHA256 {
					check.Status = "mismatch"
					check.Detail = "local artifact hash does not match"
					report.Failed++
				} else {
					check.Status = "ok"
				}
			} else if a.Verified && a.SHA256 != "" {
				check.Status = "remote-verified"
				check.Detail = "verified by runner at " + a.VerifiedAt + " on " + a.Host
			} else {
				check.Status = "remote-unverified"
				check.Detail = "runner did not record a verified SHA-256; use --strict to fail"
				if strict {
					report.Failed++
				} else {
					report.Warnings++
				}
			}
			report.Checked++
			report.Checks = append(report.Checks, check)
		}
	}
	report.OK = report.Failed == 0
	return report, nil
}

// ManifestFor is exported for tests and CLI display.
func ManifestFor(l *Ledger, runID string) (*schema.Manifest, error) {
	return LoadManifest(l, runID)
}
