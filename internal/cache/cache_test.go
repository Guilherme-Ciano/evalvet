package cache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

type fakeProvider struct {
	mu    sync.Mutex
	calls int
	err   error
	name  string
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Kind() string { return "fake" }

func (f *fakeProvider) Complete(_ context.Context, req domain.Request) (domain.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return domain.Response{}, f.err
	}
	return domain.Response{
		Text:       "answer:" + req.Model,
		StopReason: domain.StopEnd,
		Usage:      domain.Usage{InputTokens: 3, OutputTokens: 4, Known: true},
		Timing:     domain.Timing{Total: time.Second},
		Model:      req.Model,
	}, nil
}

type memStore struct {
	mu     sync.Mutex
	data   map[Key]domain.Response
	puts   int
	gets   int
	getErr error
	putErr error
}

func newMemStore() *memStore { return &memStore{data: map[Key]domain.Response{}} }

func (m *memStore) Get(_ context.Context, k Key) (domain.Response, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gets++
	if m.getErr != nil {
		return domain.Response{}, false, m.getErr
	}
	r, ok := m.data[k]
	return r, ok, nil
}

func (m *memStore) Put(_ context.Context, k Key, r domain.Response) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.putErr != nil {
		return m.putErr
	}
	m.puts++
	m.data[k] = r
	return nil
}

func req(sample int) domain.Request {
	return domain.Request{
		Model:    "m",
		Messages: []domain.Message{{Role: domain.RoleUser, Content: "hi"}},
		Sample:   sample,
	}
}

func TestParseMode(t *testing.T) {
	t.Parallel()
	for _, m := range []Mode{ModeRW, ModeRO, ModeReplay, ModeRefresh, ModeOff} {
		got, err := ParseMode(string(m))
		if err != nil || got != m {
			t.Fatalf("ParseMode(%q) = %q, %v", m, got, err)
		}
	}
	_, err := ParseMode("bogus")
	if !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("want ErrConfig, got %v", err)
	}
}

func TestModeReadsWrites(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mode          Mode
		reads, writes bool
	}{
		{ModeRW, true, true},
		{ModeRO, true, false},
		{ModeReplay, true, false},
		{ModeRefresh, false, true},
		{ModeOff, false, false},
	}
	for _, tc := range tests {
		t.Run(string(tc.mode), func(t *testing.T) {
			t.Parallel()
			if tc.mode.reads() != tc.reads || tc.mode.writes() != tc.writes {
				t.Fatalf("reads=%v writes=%v", tc.mode.reads(), tc.mode.writes())
			}
		})
	}
}

func TestComputeKey(t *testing.T) {
	t.Parallel()
	base := KeyMaterial{
		Schema:   SchemaVersion,
		Provider: "openai",
		Endpoint: "https://x",
		Model:    "m",
		Request:  map[string]any{"a": 1, "b": []any{"x"}},
	}
	k1, err := ComputeKey(base)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := ComputeKey(base)
	if err != nil {
		t.Fatal(err)
	}
	if k1 != k2 || len(k1) != 64 {
		t.Fatalf("unstable or malformed key: %q %q", k1, k2)
	}

	reordered := base
	reordered.Request = map[string]any{"b": []any{"x"}, "a": 1.0}
	if k, err := ComputeKey(reordered); err != nil || k != k1 {
		t.Fatal("key must ignore map order and numeric representation")
	}

	variants := map[string]func(KeyMaterial) KeyMaterial{
		"sample idx": func(m KeyMaterial) KeyMaterial { m.SampleIdx = 1; return m },
		"model":      func(m KeyMaterial) KeyMaterial { m.Model = "n"; return m },
		"provider":   func(m KeyMaterial) KeyMaterial { m.Provider = "anthropic"; return m },
		"endpoint":   func(m KeyMaterial) KeyMaterial { m.Endpoint = "https://y"; return m },
		"schema":     func(m KeyMaterial) KeyMaterial { m.Schema = 2; return m },
		"request":    func(m KeyMaterial) KeyMaterial { m.Request = "other"; return m },
		"extra":      func(m KeyMaterial) KeyMaterial { m.Extra = map[string]any{"k": 1}; return m },
	}
	for name, mutate := range variants {
		t.Run("changes on "+name, func(t *testing.T) {
			t.Parallel()
			k, err := ComputeKey(mutate(base))
			if err != nil {
				t.Fatal(err)
			}
			if k == k1 {
				t.Fatalf("key did not change when %s changed", name)
			}
		})
	}

	if _, err := ComputeKey(KeyMaterial{Request: make(chan int)}); err == nil {
		t.Fatal("expected error for unmarshalable request")
	}
}

func TestRequestKeyUsesSampleAndIgnoresTrial(t *testing.T) {
	t.Parallel()
	a, err := RequestKey("fake", "e", req(0))
	if err != nil {
		t.Fatal(err)
	}
	b, err := RequestKey("fake", "e", req(1))
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("sample index must change the key")
	}
	r := req(0)
	r.TrialID = "other"
	c, err := RequestKey("fake", "e", r)
	if err != nil {
		t.Fatal(err)
	}
	if a != c {
		t.Fatal("trial id must not change the key")
	}
	d, err := RequestKey("fake", "e2", req(0))
	if err != nil {
		t.Fatal(err)
	}
	if a == d {
		t.Fatal("endpoint must change the key")
	}
}

func TestFSRoundTrip(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := NewFS(root)
	ctx := context.Background()
	key, err := RequestKey("fake", "e", req(0))
	if err != nil {
		t.Fatal(err)
	}

	_, ok, err := s.Get(ctx, key)
	if err != nil || ok {
		t.Fatalf("missing key: ok=%v err=%v", ok, err)
	}

	want := domain.Response{
		Text:       "héllo <b>",
		ToolCalls:  []domain.ToolCall{{ID: "1", Name: "f", Arguments: []byte(`{"a":1}`)}},
		StopReason: domain.StopToolUse,
		Usage: domain.Usage{
			InputTokens:       1,
			CachedInputTokens: 2,
			CacheWriteTokens:  3,
			OutputTokens:      4,
			Known:             true,
		},
		Timing: domain.Timing{
			TTFB:       time.Millisecond,
			FirstToken: 2 * time.Millisecond,
			Total:      3 * time.Millisecond,
		},
		Model: "m",
	}
	if err := s.Put(ctx, key, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(ctx, key)
	if err != nil || !ok {
		t.Fatalf("get after put: ok=%v err=%v", ok, err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("round trip (-want +got):\n%s", diff)
	}

	want.Text = "overwritten"
	if err := s.Put(ctx, key, want); err != nil {
		t.Fatal(err)
	}
	got, _, err = s.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "overwritten" {
		t.Fatalf("overwrite lost: %q", got.Text)
	}

	wantPath := filepath.Join(root, "v1", string(key[:2]), string(key[2:])+".json.gz")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected file layout: %v", err)
	}
}

func TestFSAtomicWriteLeavesNoTempFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := NewFS(root)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			key, err := RequestKey("fake", "e", req(i%3))
			if err != nil {
				t.Error(err)
				return
			}
			if err := s.Put(ctx, key, domain.Response{Text: strings.Repeat("x", i)}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var stray []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && !strings.HasSuffix(p, ".json.gz") {
			stray = append(stray, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stray) != 0 {
		t.Fatalf("stray files: %v", stray)
	}
}

func TestFSGetCorruptEntry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content []byte
		wantMsg string
	}{
		{"not gzip", []byte("plain"), "cache gzip"},
		{"empty file", nil, "cache gzip"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			s := NewFS(root)
			key := Key(strings.Repeat("ab", 32))
			if err := os.MkdirAll(filepath.Dir(s.path(key)), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(s.path(key), tc.content, 0o600); err != nil {
				t.Fatal(err)
			}
			_, ok, err := s.Get(context.Background(), key)
			if ok || err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestFSGetBadJSONInsideGzip(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := NewFS(root)
	key := Key(strings.Repeat("cd", 32))
	if err := s.Put(context.Background(), key, domain.Response{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		s.path(key),
		[]byte{0x1f, 0x8b, 0x08, 0, 0, 0, 0, 0, 0, 0xff, 1, 0, 0, 0xff, 0xff, 0, 0, 0, 0, 0, 0, 0, 0},
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Get(context.Background(), key); ok || err == nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestFSPutFailsWhenRootIsFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	blocker := filepath.Join(root, "v1")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewFS(root)
	key := Key(strings.Repeat("ef", 32))
	if err := s.Put(context.Background(), key, domain.Response{}); err == nil {
		t.Fatal("expected mkdir error")
	}
	if _, _, err := s.Get(context.Background(), key); err == nil {
		t.Fatal("expected open error when a path component is a file")
	}
}

func TestWrapOffAndNilStoreReturnInner(t *testing.T) {
	t.Parallel()
	inner := &fakeProvider{name: "p"}
	if got := Wrap(inner, newMemStore(), ModeOff, "e", &Counters{}); got != domain.Provider(inner) {
		t.Fatal("off mode must return inner provider")
	}
	if got := Wrap(inner, nil, ModeRW, "e", &Counters{}); got != domain.Provider(inner) {
		t.Fatal("nil store must return inner provider")
	}
}

func TestProviderDelegation(t *testing.T) {
	t.Parallel()
	inner := &fakeProvider{name: "named"}
	p := Wrap(inner, newMemStore(), ModeRW, "e", &Counters{})
	if p.Name() != "named" || p.Kind() != "fake" {
		t.Fatalf("name=%q kind=%q", p.Name(), p.Kind())
	}
	rep, ok := p.(domain.CapabilityReporter)
	if !ok {
		t.Fatal("decorator must report capabilities")
	}
	if !rep.Capabilities().Tools {
		t.Fatalf("capabilities = %+v", rep.Capabilities())
	}
}

func TestProviderModes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tests := []struct {
		name       string
		mode       Mode
		preseed    bool
		wantCalls  int
		wantPuts   int
		wantStats  Stats
		wantErr    error
		wantHit    bool
		callsCount int
	}{
		{
			name:       "rw miss then hit",
			mode:       ModeRW,
			callsCount: 2,
			wantCalls:  1,
			wantPuts:   1,
			wantStats:  Stats{Hits: 1, Misses: 1, Writes: 1},
			wantHit:    true,
		},
		{
			name:       "rw preseeded",
			mode:       ModeRW,
			preseed:    true,
			callsCount: 1,
			wantCalls:  0,
			wantPuts:   1,
			wantStats:  Stats{Hits: 1},
			wantHit:    true,
		},
		{
			name:       "ro miss never writes",
			mode:       ModeRO,
			callsCount: 2,
			wantCalls:  2,
			wantPuts:   0,
			wantStats:  Stats{Misses: 2},
		},
		{
			name:       "ro preseeded hit",
			mode:       ModeRO,
			preseed:    true,
			callsCount: 1,
			wantCalls:  0,
			wantPuts:   1,
			wantStats:  Stats{Hits: 1},
			wantHit:    true,
		},
		{
			name:       "replay miss errors",
			mode:       ModeReplay,
			callsCount: 1,
			wantCalls:  0,
			wantPuts:   0,
			wantStats:  Stats{Misses: 1},
			wantErr:    domain.ErrCacheMiss,
		},
		{
			name:       "replay preseeded hit",
			mode:       ModeReplay,
			preseed:    true,
			callsCount: 1,
			wantCalls:  0,
			wantPuts:   1,
			wantStats:  Stats{Hits: 1},
			wantHit:    true,
		},
		{
			name:       "refresh never reads",
			mode:       ModeRefresh,
			preseed:    true,
			callsCount: 2,
			wantCalls:  2,
			wantPuts:   3,
			wantStats:  Stats{Writes: 2},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inner := &fakeProvider{name: "p"}
			store := newMemStore()
			counters := &Counters{}
			if tc.preseed {
				key, err := RequestKey("fake", "e", req(0))
				if err != nil {
					t.Fatal(err)
				}
				store.data[key] = domain.Response{Text: "seeded"}
				store.puts = 1
			}
			p := Wrap(inner, store, tc.mode, "e", counters)
			var last domain.Response
			var err error
			for range tc.callsCount {
				last, err = p.Complete(ctx, req(0))
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v want %v", err, tc.wantErr)
				}
				if !strings.Contains(err.Error(), "p ") {
					t.Fatalf("error should name provider: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if last.CacheHit != tc.wantHit {
				t.Fatalf("CacheHit = %v want %v", last.CacheHit, tc.wantHit)
			}
			if inner.calls != tc.wantCalls {
				t.Fatalf("inner calls = %d want %d", inner.calls, tc.wantCalls)
			}
			if store.puts != tc.wantPuts {
				t.Fatalf("puts = %d want %d", store.puts, tc.wantPuts)
			}
			if diff := cmp.Diff(tc.wantStats, counters.Snapshot()); diff != "" {
				t.Fatalf("stats (-want +got):\n%s", diff)
			}
			if tc.mode == ModeRefresh && store.gets != 0 {
				t.Fatalf("refresh read the store %d times", store.gets)
			}
		})
	}
}

func TestProviderHitReturnsStoredResponse(t *testing.T) {
	t.Parallel()
	inner := &fakeProvider{name: "p"}
	p := Wrap(inner, NewFS(t.TempDir()), ModeRW, "e", &Counters{})
	first, err := p.Complete(context.Background(), req(0))
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Complete(context.Background(), req(0))
	if err != nil {
		t.Fatal(err)
	}
	if first.CacheHit || !second.CacheHit {
		t.Fatalf("hit flags first=%v second=%v", first.CacheHit, second.CacheHit)
	}
	second.CacheHit = false
	if diff := cmp.Diff(first, second); diff != "" {
		t.Fatalf("cached response differs (-first +second):\n%s", diff)
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls = %d", inner.calls)
	}
}

func TestProviderSampleIdxSeparatesEntries(t *testing.T) {
	t.Parallel()
	inner := &fakeProvider{name: "p"}
	store := newMemStore()
	p := Wrap(inner, store, ModeRW, "e", &Counters{})
	for s := range 3 {
		if _, err := p.Complete(context.Background(), req(s)); err != nil {
			t.Fatal(err)
		}
	}
	if inner.calls != 3 || len(store.data) != 3 {
		t.Fatalf("calls=%d entries=%d", inner.calls, len(store.data))
	}
}

func TestProviderErrorPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	boom := errors.New("boom")

	t.Run("corrupt entry is a miss in rw mode", func(t *testing.T) {
		t.Parallel()
		inner := &fakeProvider{name: "p"}
		store := newMemStore()
		store.getErr = boom
		counters := &Counters{}
		_, err := Wrap(inner, store, ModeRW, "e", counters).Complete(ctx, req(0))
		if err != nil || inner.calls != 1 || counters.Snapshot().Corrupt != 1 {
			t.Fatalf("err=%v calls=%d stats=%+v", err, inner.calls, counters.Snapshot())
		}
	})
	t.Run("corrupt entry fails in replay mode without calling inner", func(t *testing.T) {
		t.Parallel()
		inner := &fakeProvider{name: "p"}
		store := newMemStore()
		store.getErr = boom
		_, err := Wrap(inner, store, ModeReplay, "e", &Counters{}).Complete(ctx, req(0))
		if !errors.Is(err, boom) || inner.calls != 0 {
			t.Fatalf("err=%v calls=%d", err, inner.calls)
		}
	})
	t.Run("inner error is not cached", func(t *testing.T) {
		t.Parallel()
		inner := &fakeProvider{name: "p", err: boom}
		store := newMemStore()
		counters := &Counters{}
		_, err := Wrap(inner, store, ModeRW, "e", counters).Complete(ctx, req(0))
		if !errors.Is(err, boom) || len(store.data) != 0 || counters.Snapshot().Writes != 0 {
			t.Fatalf("err=%v entries=%d stats=%+v", err, len(store.data), counters.Snapshot())
		}
	})
	t.Run("put error returns response and error", func(t *testing.T) {
		t.Parallel()
		inner := &fakeProvider{name: "p"}
		store := newMemStore()
		store.putErr = boom
		counters := &Counters{}
		resp, err := Wrap(inner, store, ModeRW, "e", counters).Complete(ctx, req(0))
		if !errors.Is(err, boom) || resp.Text == "" || counters.Snapshot().Writes != 0 {
			t.Fatalf("err=%v resp=%+v stats=%+v", err, resp, counters.Snapshot())
		}
	})
	t.Run("unmarshalable request fails key computation", func(t *testing.T) {
		t.Parallel()
		inner := &fakeProvider{name: "p"}
		r := req(0)
		r.Tools = []domain.ToolDef{{Name: "t", Parameters: []byte("{bad")}}
		_, err := Wrap(inner, newMemStore(), ModeRW, "e", &Counters{}).Complete(ctx, r)
		if err == nil || inner.calls != 0 {
			t.Fatalf("err=%v calls=%d", err, inner.calls)
		}
	})
}

func TestProviderConcurrentCountersRace(t *testing.T) {
	t.Parallel()
	inner := &fakeProvider{name: "p"}
	counters := &Counters{}
	p := Wrap(inner, newMemStore(), ModeRW, "e", counters)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			if _, err := p.Complete(context.Background(), req(i%4)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	st := counters.Snapshot()
	if st.Hits+st.Misses != 16 || st.Writes < 4 {
		t.Fatalf("stats = %+v", st)
	}
}
