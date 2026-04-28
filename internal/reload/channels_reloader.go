package reload

import (
	"context"
	"log"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/gateway"
)

const channelsReloadDebounce = 300 * time.Millisecond

// ChannelsReloader watches channels.yml for changes and reconciles the gateway registry.
type ChannelsReloader struct {
	channelsPath string
	cfg         config.Config
	registry    *gateway.Registry
	watcher     *fsnotify.Watcher
	stopCh      chan struct{}
	doneCh      chan struct{}
	started     bool
}

// NewChannelsReloader creates a ChannelsReloader but does not start watching.
// Call Start() to begin.
func NewChannelsReloader(channelsPath string, cfg config.Config, reg *gateway.Registry) *ChannelsReloader {
	return &ChannelsReloader{
		channelsPath: channelsPath,
		cfg:         cfg,
		registry:    reg,
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
	}
}

// Start begins watching for file changes in the background.
func (r *ChannelsReloader) Start(ctx context.Context) {
	dir := filepath.Dir(r.channelsPath)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("[channels-reload] fsnotify init failed: %v", err)
		close(r.doneCh)
		return
	}
	r.watcher = watcher

	if err := watcher.Add(dir); err != nil {
		log.Printf("[channels-reload] watch add %s failed: %v", dir, err)
		_ = watcher.Close()
		close(r.doneCh)
		return
	}
	log.Printf("[channels-reload] watching: %s", dir)

	r.started = true
	go r.run(ctx)
}

// run handles fsnotify events with debouncing.
func (r *ChannelsReloader) run(ctx context.Context) {
	defer func() {
		_ = r.watcher.Close()
		close(r.doneCh)
	}()

	var timer *time.Timer
	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-r.stopCh:
			if timer != nil {
				timer.Stop()
			}
			return
		case event, ok := <-r.watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			if filepath.Base(event.Name) != filepath.Base(r.channelsPath) {
				continue
			}
			log.Printf("[channels-reload] change detected: %s", filepath.Base(event.Name))
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(channelsReloadDebounce, func() {
				r.reload()
			})
		case err, ok := <-r.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("[channels-reload] watcher error: %v", err)
		}
	}
}

// reload reads channels.yml and reconciles the gateway registry.
func (r *ChannelsReloader) reload() {
	entries, err := config.LoadChannelsOnly(r.cfg, r.channelsPath)
	if err != nil {
		log.Printf("[channels-reload] failed to load channels.yml: %v", err)
		return
	}
	if entries == nil {
		entries = []config.GatewayEntry{}
	}
	if err := r.registry.Reconcile(entries); err != nil {
		log.Printf("[channels-reload] reconcile failed: %v", err)
	}
}

// Stop stops the watcher.
func (r *ChannelsReloader) Stop() {
	if !r.started {
		return
	}
	close(r.stopCh)
	<-r.doneCh
}
