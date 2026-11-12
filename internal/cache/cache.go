package cache

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

const SchemaVersion = 1

var (
	errTrailing  = errors.New("trailing data")
	errNonFinite = errors.New("non-finite number")
)

type Mode string

const (
	ModeRW      Mode = "rw"
	ModeRO      Mode = "ro"
	ModeReplay  Mode = "replay"
	ModeRefresh Mode = "refresh"
	ModeOff     Mode = "off"
)

func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case ModeRW, ModeRO, ModeReplay, ModeRefresh, ModeOff:
		return m, nil
	}
	return "", fmt.Errorf("unknown cache mode %q (want rw, ro, replay, refresh or off): %w", s, domain.ErrConfig)
}

func (m Mode) reads() bool  { return m == ModeRW || m == ModeRO || m == ModeReplay }
func (m Mode) writes() bool { return m == ModeRW || m == ModeRefresh }

type KeyMaterial struct {
	Schema    int            `json:"schema"`
	Provider  string         `json:"provider"`
	Endpoint  string         `json:"endpoint"`
	Model     string         `json:"model"`
	Request   any            `json:"request"`
	SampleIdx int            `json:"sample_idx"`
	Extra     map[string]any `json:"extra,omitempty"`
}

type Key string

func ComputeKey(m KeyMaterial) (Key, error) {
	b, err := Canonical(m)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return Key(hex.EncodeToString(sum[:])), nil
}

type Store interface {
	Get(ctx context.Context, key Key) (domain.Response, bool, error)
	Put(ctx context.Context, key Key, resp domain.Response) error
}

type FS struct {
	root string
}

func NewFS(root string) *FS {
	return &FS{root: filepath.Join(root, fmt.Sprintf("v%d", SchemaVersion))}
}

func (s *FS) path(k Key) string {
	return filepath.Join(s.root, string(k[:2]), string(k[2:])+".json.gz")
}

func (s *FS) Get(_ context.Context, k Key) (domain.Response, bool, error) {
	f, err := os.Open(s.path(k))
	if errors.Is(err, fs.ErrNotExist) {
		return domain.Response{}, false, nil
	}
	if err != nil {
		return domain.Response{}, false, fmt.Errorf("cache open: %w", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return domain.Response{}, false, fmt.Errorf("cache gzip: %w", err)
	}
	defer zr.Close()
	var r domain.Response
	if err := json.NewDecoder(zr).Decode(&r); err != nil {
		return domain.Response{}, false, fmt.Errorf("cache decode: %w", err)
	}
	return r, true, nil
}

func (s *FS) Put(_ context.Context, k Key, resp domain.Response) error {
	p := s.path(k)
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("cache mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("cache temp: %w", err)
	}
	name := tmp.Name()
	zw := gzip.NewWriter(tmp)
	encErr := json.NewEncoder(zw).Encode(resp)
	zErr := zw.Close()
	cErr := tmp.Close()
	if err := errors.Join(encErr, zErr, cErr); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("cache write: %w", err)
	}
	if err := os.Rename(name, p); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("cache rename: %w", err)
	}
	return nil
}

type Stats struct {
	Hits   int64
	Misses int64
	Writes int64
}

type Counters struct {
	hits, misses, writes atomic.Int64
}

func (c *Counters) Snapshot() Stats {
	return Stats{Hits: c.hits.Load(), Misses: c.misses.Load(), Writes: c.writes.Load()}
}

type Provider struct {
	inner    domain.Provider
	store    Store
	mode     Mode
	endpoint string
	counters *Counters
}

func Wrap(inner domain.Provider, store Store, mode Mode, endpoint string, counters *Counters) domain.Provider {
	if mode == ModeOff || store == nil {
		return inner
	}
	return &Provider{inner: inner, store: store, mode: mode, endpoint: endpoint, counters: counters}
}

func (p *Provider) Name() string { return p.inner.Name() }
func (p *Provider) Kind() string { return p.inner.Kind() }

func (p *Provider) Capabilities() domain.Capabilities {
	return domain.CapabilitiesOf(p.inner)
}

func RequestKey(kind, endpoint string, req domain.Request) (Key, error) {
	return ComputeKey(KeyMaterial{
		Schema:    SchemaVersion,
		Provider:  kind,
		Endpoint:  endpoint,
		Model:     req.Model,
		Request:   req,
		SampleIdx: req.Sample,
	})
}

func (p *Provider) Complete(ctx context.Context, req domain.Request) (domain.Response, error) {
	key, err := RequestKey(p.inner.Kind(), p.endpoint, req)
	if err != nil {
		return domain.Response{}, err
	}
	if p.mode.reads() {
		resp, ok, err := p.store.Get(ctx, key)
		if err != nil {
			return domain.Response{}, err
		}
		if ok {
			p.counters.hits.Add(1)
			resp.CacheHit = true
			return resp, nil
		}
		p.counters.misses.Add(1)
		if p.mode == ModeReplay {
			return domain.Response{}, fmt.Errorf("%s %s: %w", p.inner.Name(), key[:12], domain.ErrCacheMiss)
		}
	}
	resp, err := p.inner.Complete(ctx, req)
	if err != nil {
		return resp, err
	}
	if p.mode.writes() {
		if err := p.store.Put(ctx, key, resp); err != nil {
			return resp, err
		}
		p.counters.writes.Add(1)
	}
	return resp, nil
}
