package baseline

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

const Schema = 1

var ErrNotFound = errors.New("baseline not found")

type Baseline struct {
	Schema         int                  `json:"schema"`
	Suite          string               `json:"suite"`
	CreatedAt      time.Time            `json:"created_at"`
	GitSHA         string               `json:"git_sha,omitempty"`
	ConfigHash     string               `json:"config_hash"`
	PricingVersion string               `json:"pricing_version"`
	Version        string               `json:"evalvet_version"`
	Targets        []domain.TargetStats `json:"targets"`
}

func FromRun(r domain.RunResult, gitSHA string, now time.Time) Baseline {
	return Baseline{
		Schema:         Schema,
		Suite:          r.Suite,
		CreatedAt:      now.UTC().Truncate(time.Second),
		GitSHA:         gitSHA,
		ConfigHash:     r.ConfigHash,
		PricingVersion: r.Method.PricingVersion,
		Version:        r.Version,
		Targets:        r.Targets,
	}
}

func (b Baseline) Target(name string) (domain.TargetStats, bool) {
	for _, t := range b.Targets {
		if t.Target == name {
			return t, true
		}
	}
	return domain.TargetStats{}, false
}

func Load(path string) (Baseline, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Baseline{}, fmt.Errorf("%s: %w", path, ErrNotFound)
	}
	if err != nil {
		return Baseline{}, fmt.Errorf("read baseline: %w", err)
	}
	return Parse(raw)
}

func Parse(raw []byte) (Baseline, error) {
	var b Baseline
	if err := json.Unmarshal(raw, &b); err != nil {
		return Baseline{}, fmt.Errorf("decode baseline: %w", err)
	}
	if b.Schema != Schema {
		return Baseline{}, fmt.Errorf("baseline schema %d, want %d: %w", b.Schema, Schema, domain.ErrConfig)
	}
	return b, nil
}

func Save(path string, b Baseline) error {
	out, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("encode baseline: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("baseline dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".baseline-*")
	if err != nil {
		return fmt.Errorf("baseline temp: %w", err)
	}
	_, wErr := tmp.Write(append(out, '\n'))
	cErr := tmp.Close()
	if err := errors.Join(wErr, cErr); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write baseline: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("rename baseline: %w", err)
	}
	return nil
}
