package watch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWatcherRecursesAndWatchesCreatedDirs(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "existing")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("mkdir existing: %v", err)
	}
	changes := make(chan string, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watcher, err := Start(ctx, Spec{
		LogPrefix: "[watch-test]",
		Roots:     []Root{{Path: root, Label: "root", Recursive: true}},
		Debounce:  20 * time.Millisecond,
		OnEvent: func(event Event) {
			changes <- filepath.Base(event.Path)
		},
		OnDebounce: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer waitDone(t, cancel, watcher)
	time.Sleep(100 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(existing, "one.yml"), []byte("one"), 0o644); err != nil {
		t.Fatalf("write existing child: %v", err)
	}
	assertSawChange(t, changes, "one.yml")

	created := filepath.Join(root, "created")
	if err := os.MkdirAll(created, 0o755); err != nil {
		t.Fatalf("mkdir created: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(created, "two.yml"), []byte("two"), 0o644); err != nil {
		t.Fatalf("write created child: %v", err)
	}
	assertSawChange(t, changes, "two.yml")
}

func TestWatcherDebouncesAndFilters(t *testing.T) {
	root := t.TempDir()
	debounced := make(chan struct{}, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watcher, err := Start(ctx, Spec{
		LogPrefix: "[watch-test]",
		Roots:     []Root{{Path: root, Label: "root", Recursive: true}},
		Debounce:  50 * time.Millisecond,
		Ignore: func(path string) bool {
			return strings.HasSuffix(path, ".DS_Store")
		},
		Include: func(event Event) bool {
			return strings.HasSuffix(event.Path, ".yml")
		},
		OnDebounce: func(context.Context) error {
			debounced <- struct{}{}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer waitDone(t, cancel, watcher)
	time.Sleep(100 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(root, ".DS_Store"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("write ignored: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("write filtered: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "one.yml"), []byte("one"), 0o644); err != nil {
		t.Fatalf("write one: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "two.yml"), []byte("two"), 0o644); err != nil {
		t.Fatalf("write two: %v", err)
	}

	select {
	case <-debounced:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for debounce")
	}
	select {
	case <-debounced:
		t.Fatal("expected yml writes in one debounce window to trigger once")
	case <-time.After(150 * time.Millisecond):
	}
}

func assertSawChange(t *testing.T, changes <-chan string, want string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-changes:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for change %q", want)
		}
	}
}

func waitDone(t *testing.T, cancel context.CancelFunc, watcher *Watcher) {
	t.Helper()
	cancel()
	select {
	case <-watcher.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watcher stop")
	}
}
