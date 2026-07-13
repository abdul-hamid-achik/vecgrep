package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
	"github.com/abdul-hamid-achik/veclite"
)

type coordinatorProvider struct {
	dimensions int
	model      string

	pingCalls   atomic.Int32
	firstPing   chan struct{}
	secondPing  chan struct{}
	releasePing chan struct{}
}

func (p *coordinatorProvider) Embed(context.Context, string) ([]float32, error) {
	return make([]float32, p.dimensions), nil
}

func (p *coordinatorProvider) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for i := range result {
		result[i] = make([]float32, p.dimensions)
	}
	return result, nil
}

func (p *coordinatorProvider) Model() string   { return p.model }
func (p *coordinatorProvider) Dimensions() int { return p.dimensions }

func (p *coordinatorProvider) Ping(ctx context.Context) error {
	switch p.pingCalls.Add(1) {
	case 1:
		if p.firstPing != nil {
			close(p.firstPing)
		}
		if p.releasePing != nil {
			select {
			case <-p.releasePing:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	case 2:
		if p.secondPing != nil {
			close(p.secondPing)
		}
	}
	return nil
}

func (p *coordinatorProvider) Warmup(context.Context) (time.Duration, error) {
	return 0, nil
}

type trackingIndexDBSource struct {
	database   *db.DB
	releaseErr error
	onRelease  func()

	mu           sync.Mutex
	acquisitions int
	releases     int
	active       int
	maxActive    int
}

func (s *trackingIndexDBSource) AcquireIndexDB(context.Context) (IndexDBLease, error) {
	s.mu.Lock()
	s.acquisitions++
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.mu.Unlock()
	return IndexDBLease{
		DB: s.database,
		Release: func() error {
			if s.onRelease != nil {
				s.onRelease()
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			s.releases++
			s.active--
			return s.releaseErr
		},
	}, nil
}

func TestIndexCoordinatorFinalizesReceiptBeforeReleasingWriter(t *testing.T) {
	root, cfg, database := newCoordinatorFixture(t)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	provider := &coordinatorProvider{dimensions: cfg.Embedding.Dimensions, model: cfg.Embedding.Model}
	var (
		atRelease *IngestionReceipt
		loadErr   error
	)
	stores := &trackingIndexDBSource{database: database}
	stores.onRelease = func() {
		atRelease, loadErr = LoadIngestionReceipt(cfg.DataDir, root)
	}
	coordinator := NewIndexCoordinator(root, cfg, provider, stores)

	if _, err := coordinator.Index(context.Background(), IndexRequest{StructuralChunks: string(StructuralChunksOff)}, nil); err != nil {
		t.Fatal(err)
	}
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if atRelease == nil || !atRelease.Success || !atRelease.Complete || !atRelease.IngestionComplete {
		t.Fatalf("receipt at writer release = %+v", atRelease)
	}
}

func TestIndexCoordinatorRequiresFullReindexToRecoverDirtyProject(t *testing.T) {
	root, cfg, database := newCoordinatorFixture(t)
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	provider := &coordinatorProvider{dimensions: cfg.Embedding.Dimensions, model: cfg.Embedding.Model}
	coordinator := NewIndexCoordinator(root, cfg, provider, &trackingIndexDBSource{database: database})
	if _, err := coordinator.Index(context.Background(), IndexRequest{StructuralChunks: string(StructuralChunksOff)}, nil); err != nil {
		t.Fatalf("initial coordinator index: %v", err)
	}

	// Simulate the durable residue left by an interrupted multi-collection
	// mutation. Seed the persisted contract record directly, then exercise only
	// the public coordinator API for both rejection and recovery.
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	persistDirtyProjectMarker(t, cfg.DataDir, root)
	reopened, err := db.Open("", cfg.Embedding.Dimensions, cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	coordinator = NewIndexCoordinator(root, cfg, provider, &trackingIndexDBSource{database: reopened})

	receiptBefore, err := LoadIngestionReceipt(cfg.DataDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if receiptBefore == nil || !receiptBefore.Success || !receiptBefore.Complete || !receiptBefore.IngestionComplete {
		t.Fatalf("receipt before rejected incremental = %+v", receiptBefore)
	}
	statsBefore, err := reopened.StatsForProject(root)
	if err != nil {
		t.Fatal(err)
	}
	chunksBefore, err := reopened.GetChunksByFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	chunksBefore = sortedChunkRecordsByID(chunksBefore)
	sourceHashesBefore, sourceCompleteBefore, err := reopened.GetSourceHashes(root)
	if err != nil {
		t.Fatal(err)
	}
	if sourceCompleteBefore {
		t.Fatal("dirty project reported complete source hashes before rejected incremental")
	}

	_, err = coordinator.Index(context.Background(), IndexRequest{StructuralChunks: string(StructuralChunksOff)}, nil)
	if !errors.Is(err, db.ErrProjectFileHashesDirty) {
		t.Fatalf("incremental coordinator index error = %v, want dirty sentinel", err)
	}
	if !strings.Contains(err.Error(), "vecgrep index --full") || !strings.Contains(err.Error(), "force:true") {
		t.Fatalf("incremental coordinator recovery hint = %v", err)
	}
	receiptAfter, err := LoadIngestionReceipt(cfg.DataDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if receiptAfter == nil || receiptAfter.AttemptID != receiptBefore.AttemptID || receiptAfter.Success != receiptBefore.Success || receiptAfter.Complete != receiptBefore.Complete || !reflect.DeepEqual(receiptAfter, receiptBefore) {
		t.Fatalf("rejected incremental changed receipt:\n before=%+v\n after=%+v", receiptBefore, receiptAfter)
	}
	statsAfter, err := reopened.StatsForProject(root)
	if err != nil {
		t.Fatal(err)
	}
	chunksAfter, err := reopened.GetChunksByFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	chunksAfter = sortedChunkRecordsByID(chunksAfter)
	sourceHashesAfter, sourceCompleteAfter, err := reopened.GetSourceHashes(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(statsAfter, statsBefore) || !reflect.DeepEqual(chunksAfter, chunksBefore) || !reflect.DeepEqual(sourceHashesAfter, sourceHashesBefore) || sourceCompleteAfter != sourceCompleteBefore {
		t.Fatalf("rejected incremental mutated index:\n stats before=%v after=%v\n chunks before=%v after=%v\n source hashes before=%v/%t after=%v/%t",
			statsBefore, statsAfter, chunksBefore, chunksAfter, sourceHashesBefore, sourceCompleteBefore, sourceHashesAfter, sourceCompleteAfter)
	}

	if _, err := coordinator.Index(context.Background(), IndexRequest{FullReindex: true, StructuralChunks: string(StructuralChunksOff)}, nil); err != nil {
		t.Fatalf("full coordinator recovery: %v", err)
	}
	if _, err := reopened.GetFileHashes(root); err != nil {
		t.Fatalf("file hashes remain dirty after full coordinator recovery: %v", err)
	}
	if _, complete, err := reopened.GetSourceHashes(root); err != nil || !complete {
		t.Fatalf("source hashes after full coordinator recovery: complete=%t err=%v", complete, err)
	}
	receipt, err := LoadIngestionReceipt(cfg.DataDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if receipt == nil || !receipt.Success || !receipt.Complete || !receipt.IngestionComplete || !receipt.ScopeComplete {
		t.Fatalf("recovery receipt = %+v", receipt)
	}
}

func persistDirtyProjectMarker(t *testing.T, dataDir, projectRoot string) {
	t.Helper()
	raw, err := veclite.Open(db.VecLitePath(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	coll, err := raw.GetCollection("file_hashes")
	if err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	key := "dirty:" + projectRoot
	if _, _, err := coll.UpsertRecordByKey("_file_hash_key", key, veclite.RecordInput{Payload: map[string]any{
		"_file_hash_key": key,
		"_record_type":   "project_dirty",
		"project_root":   projectRoot,
	}}); err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	if err := raw.Sync(); err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
}

func sortedChunkRecordsByID(records []db.ChunkRecord) []db.ChunkRecord {
	canonical := append([]db.ChunkRecord(nil), records...)
	sort.Slice(canonical, func(i, j int) bool {
		return canonical[i].ID < canonical[j].ID
	})
	return canonical
}

func TestIndexCoordinatorBeginReceiptFailureAbortsBeforeMutation(t *testing.T) {
	root, cfg, database := newCoordinatorFixture(t)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	blockedDataDir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedDataDir, []byte("block receipt parent"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = blockedDataDir
	provider := &coordinatorProvider{dimensions: cfg.Embedding.Dimensions, model: cfg.Embedding.Model}
	stores := &trackingIndexDBSource{database: database}
	coordinator := NewIndexCoordinator(root, cfg, provider, stores)

	_, err := coordinator.Index(context.Background(), IndexRequest{StructuralChunks: string(StructuralChunksOff)}, nil)
	if err == nil || !strings.Contains(err.Error(), "begin ingestion receipt") {
		t.Fatalf("Index error = %v, want begin receipt failure", err)
	}
	stats, statsErr := database.StatsForProject(root)
	if statsErr != nil {
		t.Fatal(statsErr)
	}
	if stats["chunks"] != 0 || stats["files"] != 0 {
		t.Fatalf("index mutated before receipt invalidation: %v", stats)
	}
}

func TestIndexCoordinatorPoisonsReceiptBeforeProfilePreflightFailure(t *testing.T) {
	root, cfg, database := newCoordinatorFixture(t)
	oldAttempt := startReceiptAttempt(t, cfg.DataDir, root, StructuralChunksOff, true)
	if err := recordIngestionReceipt(cfg.DataDir, structuralChunksSetup{RequestedMode: StructuralChunksOff}, index.IndexRunReport{
		AttemptID:     oldAttempt,
		ScopeComplete: true,
		ProjectRoot:   root,
		FinishedAt:    time.Now().Add(-time.Minute),
		Result:        &index.IndexResult{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := finalizeIngestionReceiptAttempt(cfg.DataDir, root, oldAttempt, nil); err != nil {
		t.Fatal(err)
	}
	mismatched := CurrentEmbeddingProfile(cfg)
	mismatched.Model = "different-model"
	if err := SaveEmbeddingProfile(database, cfg.DataDir, mismatched); err != nil {
		t.Fatal(err)
	}

	provider := &coordinatorProvider{dimensions: cfg.Embedding.Dimensions, model: cfg.Embedding.Model}
	coordinator := NewIndexCoordinator(root, cfg, provider, &trackingIndexDBSource{database: database})
	_, err := coordinator.Index(context.Background(), IndexRequest{StructuralChunks: string(StructuralChunksOff)}, nil)
	var mismatch *EmbeddingProfileMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Index error = %v, want embedding profile mismatch", err)
	}
	receipt, loadErr := LoadIngestionReceipt(cfg.DataDir, root)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if receipt == nil || receipt.AttemptID == oldAttempt || receipt.Success || receipt.Complete || receipt.IngestionComplete {
		t.Fatalf("receipt after profile preflight failure = %+v", receipt)
	}
	if len(receipt.Fallbacks) != 1 || receipt.Fallbacks[0].Code != "postflight_failed" {
		t.Fatalf("profile failure receipt fallbacks = %+v", receipt.Fallbacks)
	}
}

func TestIndexCoordinatorReleaseFailureDoesNotRevokeSupersedingAttempt(t *testing.T) {
	root, cfg, database := newCoordinatorFixture(t)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	releaseErr := errors.New("release failed")
	var (
		newerAttempt string
		beginErr     error
	)
	stores := &trackingIndexDBSource{database: database, releaseErr: releaseErr}
	stores.onRelease = func() {
		newerAttempt, beginErr = newIngestionAttemptID()
		if beginErr == nil {
			beginErr = beginIngestionReceiptAttempt(cfg.DataDir, root, newerAttempt, StructuralChunksOff, true)
		}
	}
	provider := &coordinatorProvider{dimensions: cfg.Embedding.Dimensions, model: cfg.Embedding.Model}
	coordinator := NewIndexCoordinator(root, cfg, provider, stores)

	_, err := coordinator.Index(context.Background(), IndexRequest{StructuralChunks: string(StructuralChunksOff)}, nil)
	if beginErr != nil {
		t.Fatal(beginErr)
	}
	if !errors.Is(err, releaseErr) || !errors.Is(err, errIngestionAttemptSuperseded) {
		t.Fatalf("Index error = %v, want release and superseded errors", err)
	}
	receipt, loadErr := LoadIngestionReceipt(cfg.DataDir, root)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if receipt == nil || receipt.AttemptID != newerAttempt || receipt.Success || receipt.Complete || receipt.IngestionComplete {
		t.Fatalf("superseding receipt was revoked = %+v", receipt)
	}
}

func TestIndexCoordinatorPartialRunLeavesGlobalReceiptUntrusted(t *testing.T) {
	root, cfg, database := newCoordinatorFixture(t)
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	provider := &coordinatorProvider{dimensions: cfg.Embedding.Dimensions, model: cfg.Embedding.Model}
	stores := &trackingIndexDBSource{database: database}
	coordinator := NewIndexCoordinator(root, cfg, provider, stores)
	if _, err := coordinator.Index(context.Background(), IndexRequest{StructuralChunks: string(StructuralChunksOff)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package main\nfunc main() { println(1) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Index(context.Background(), IndexRequest{Paths: []string{path}, StructuralChunks: string(StructuralChunksOff)}, nil); err != nil {
		t.Fatal(err)
	}
	receipt, err := LoadIngestionReceipt(cfg.DataDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if receipt == nil || receipt.ScopeComplete || receipt.IngestionComplete || receipt.Success || receipt.Complete {
		t.Fatalf("partial run receipt = %+v", receipt)
	}
}

func (s *trackingIndexDBSource) counts() (acquisitions, releases, active, maxActive int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquisitions, s.releases, s.active, s.maxActive
}

func newCoordinatorFixture(t *testing.T) (string, *config.Config, *db.DB) {
	t.Helper()
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = filepath.Join(root, config.DefaultDataDir)
	cfg.Embedding.Dimensions = 8
	cfg.Codemap.StructuralChunks = string(StructuralChunksOff)
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open("", cfg.Embedding.Dimensions, cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return root, cfg, database
}

func TestIndexCoordinatorSerializesRunsAndReleasesEachLease(t *testing.T) {
	root, cfg, database := newCoordinatorFixture(t)
	provider := &coordinatorProvider{
		dimensions:  cfg.Embedding.Dimensions,
		model:       cfg.Embedding.Model,
		firstPing:   make(chan struct{}),
		secondPing:  make(chan struct{}),
		releasePing: make(chan struct{}),
	}
	stores := &trackingIndexDBSource{database: database}
	coordinator := NewIndexCoordinator(root, cfg, provider, stores)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errCh := make(chan error, 2)
	go func() {
		_, err := coordinator.Index(ctx, IndexRequest{StructuralChunks: string(StructuralChunksOff)}, nil)
		errCh <- err
	}()
	select {
	case <-provider.firstPing:
	case <-ctx.Done():
		t.Fatal("first index did not reach provider ping")
	}
	go func() {
		_, err := coordinator.Index(ctx, IndexRequest{StructuralChunks: string(StructuralChunksOff)}, nil)
		errCh <- err
	}()

	select {
	case <-provider.secondPing:
		t.Fatal("second index entered the provider while the first run held the coordinator")
	case <-time.After(100 * time.Millisecond):
	}
	close(provider.releasePing)
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("Index() error = %v", err)
		}
	}
	select {
	case <-provider.secondPing:
	default:
		t.Fatal("second index never ran after the first completed")
	}
	acquisitions, releases, active, maxActive := stores.counts()
	if acquisitions != 2 || releases != 2 || active != 0 || maxActive != 1 {
		t.Fatalf("lease counts = acquire:%d release:%d active:%d max:%d", acquisitions, releases, active, maxActive)
	}
}

func TestIndexCoordinatorJoinsReleaseErrorOnEarlyReturn(t *testing.T) {
	root, cfg, database := newCoordinatorFixture(t)
	releaseErr := errors.New("release failed")
	stores := &trackingIndexDBSource{database: database, releaseErr: releaseErr}
	provider := &coordinatorProvider{dimensions: cfg.Embedding.Dimensions, model: cfg.Embedding.Model}
	coordinator := NewIndexCoordinator(root, cfg, provider, stores)

	_, err := coordinator.Index(context.Background(), IndexRequest{StructuralChunks: "invalid"}, nil)
	if err == nil || !errors.Is(err, releaseErr) {
		t.Fatalf("Index() error = %v, want joined release error", err)
	}
	_, releases, active, _ := stores.counts()
	if releases != 1 || active != 0 {
		t.Fatalf("release count = %d, active = %d, want exactly one release", releases, active)
	}
}

func TestIndexCoordinatorReleasesNilDatabaseLease(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Embedding.Dimensions = 8
	releaseErr := errors.New("nil lease release failed")
	stores := &trackingIndexDBSource{releaseErr: releaseErr}
	provider := &coordinatorProvider{dimensions: cfg.Embedding.Dimensions, model: cfg.Embedding.Model}
	coordinator := NewIndexCoordinator(root, cfg, provider, stores)

	_, err := coordinator.Index(context.Background(), IndexRequest{}, nil)
	if err == nil || !errors.Is(err, releaseErr) {
		t.Fatalf("Index() error = %v, want nil database and release errors", err)
	}
	_, releases, active, _ := stores.counts()
	if releases != 1 || active != 0 {
		t.Fatalf("release count = %d, active = %d, want exactly one release", releases, active)
	}
}

func TestIndexCoordinatorAppliesProjectScopedWatchDelete(t *testing.T) {
	root, cfg, database := newCoordinatorFixture(t)
	chunk := db.NewChunkRecord(
		filepath.Join(root, "main.go"), "main.go", "hash", 12, "go",
		"package main", 1, 1, 0, 12, "generic", "", root,
	)
	if _, err := database.InsertChunk(chunk, make([]float32, cfg.Embedding.Dimensions)); err != nil {
		t.Fatal(err)
	}
	stores := &trackingIndexDBSource{database: database}
	coordinator := NewIndexCoordinator(root, cfg, nil, stores)

	result, err := coordinator.ApplyWatchEvents(context.Background(), []index.WatchEvent{{
		Path: filepath.Join(root, "main.go"),
		Op:   index.OpRemove,
	}})
	if err != nil {
		t.Fatalf("ApplyWatchEvents() error = %v", err)
	}
	if result.FilesDeleted != 1 {
		t.Fatalf("FilesDeleted = %d, want 1", result.FilesDeleted)
	}
	hashes, err := database.GetFileHashes(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(hashes) != 0 {
		t.Fatalf("project hashes after delete = %v", hashes)
	}
	acquisitions, releases, active, _ := stores.counts()
	if acquisitions != 1 || releases != 1 || active != 0 {
		t.Fatalf("lease counts = acquire:%d release:%d active:%d", acquisitions, releases, active)
	}
}
