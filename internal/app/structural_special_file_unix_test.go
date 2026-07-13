//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

func TestStructuralFIFOFilesRespectAutoAndRequiredWithoutBlocking(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		viaLink bool
	}{
		{name: "direct_fifo"},
		{name: "symlink_fifo", viaLink: true},
	} {
		for _, required := range []bool{false, true} {
			mode := "auto"
			if required {
				mode = "required"
			}
			t.Run(fixture.name+"_"+mode, func(t *testing.T) {
				root, cfg, database := newCoordinatorFixture(t)
				const rel = "blocked.go"
				fifoPath := filepath.Join(root, rel)
				if fixture.viaLink {
					target := filepath.Join(root, "blocked.pipe")
					if err := syscall.Mkfifo(target, 0o600); err != nil {
						t.Skipf("create FIFO: %v", err)
					}
					if err := os.Symlink("blocked.pipe", fifoPath); err != nil {
						t.Skipf("create FIFO symlink: %v", err)
					}
				} else if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
					t.Skipf("create FIFO: %v", err)
				}

				projectKey, err := structuralProjectKey(root)
				if err != nil {
					t.Fatal(err)
				}
				fingerprint := strings.Repeat("a", 64)
				record := validStructuralRecord(projectKey, fingerprint, rel, "Blocked", "package blocked")
				source := singleStructuralPageSource(projectKey, fingerprint, []structuralSymbolRecord{record})
				idxCfg := BuildIndexerConfig(cfg, nil)
				idxCfg.Workers = 1
				idx := index.NewIndexer(database, &coordinatorProvider{dimensions: cfg.Embedding.Dimensions, model: cfg.Embedding.Model}, idxCfg)
				idx.SetStructuralChunkSource(source, required)

				outcome := runStructuralIndexWithTimeout(t, idx, root)
				if required {
					if outcome.err == nil || !strings.Contains(outcome.err.Error(), "not a regular file") || !strings.Contains(outcome.err.Error(), "load codemap structural chunks") {
						t.Fatalf("required FIFO result/error = %+v / %v", outcome.result, outcome.err)
					}
					return
				}
				if outcome.err != nil || outcome.result == nil {
					t.Fatalf("auto FIFO result/error = %+v / %v", outcome.result, outcome.err)
				}
				warning := errors.Join(outcome.result.Errors...)
				if warning == nil || !strings.Contains(warning.Error(), "not a regular file") || !strings.Contains(warning.Error(), "using built-in chunker") {
					t.Fatalf("auto FIFO warning = %v", warning)
				}
			})
		}
	}
}

func TestStructuralSymlinkToRegularFileRemainsSupported(t *testing.T) {
	root, cfg, database := newCoordinatorFixture(t)
	const (
		targetRel = "target.go"
		aliasRel  = "alias.go"
		content   = "package fixture\n\nfunc Linked() {}"
	)
	writeStructuralFixture(t, root, targetRel, content)
	if err := os.Symlink(targetRel, filepath.Join(root, aliasRel)); err != nil {
		t.Skipf("create regular-file symlink: %v", err)
	}
	projectKey, err := structuralProjectKey(root)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := strings.Repeat("a", 64)
	record := validStructuralRecord(projectKey, fingerprint, aliasRel, "Linked", content)
	record.EndLine = 3
	source := singleStructuralPageSource(projectKey, fingerprint, []structuralSymbolRecord{record})
	idxCfg := BuildIndexerConfig(cfg, nil)
	idxCfg.Workers = 1
	idx := index.NewIndexer(database, &coordinatorProvider{dimensions: cfg.Embedding.Dimensions, model: cfg.Embedding.Model}, idxCfg)
	idx.SetStructuralChunkSource(source, true)

	outcome := runStructuralIndexWithTimeout(t, idx, root)
	if outcome.err != nil || outcome.result == nil || len(outcome.result.Errors) != 0 {
		t.Fatalf("regular symlink result/error = %+v / %v", outcome.result, outcome.err)
	}
	chunks, err := database.GetChunksByFile(aliasRel)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) == 0 || chunks[0].Content != content {
		t.Fatalf("regular symlink chunks = %+v", chunks)
	}
}

type structuralIndexOutcome struct {
	result *index.IndexResult
	err    error
}

func runStructuralIndexWithTimeout(t *testing.T, idx *index.Indexer, root string) structuralIndexOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan structuralIndexOutcome, 1)
	go func() {
		result, err := idx.Index(ctx, root)
		done <- structuralIndexOutcome{result: result, err: err}
	}()
	select {
	case outcome := <-done:
		return outcome
	case <-ctx.Done():
		t.Fatal("structural source blocked while opening a special file")
		return structuralIndexOutcome{}
	}
}
