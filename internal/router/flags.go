package router

import (
	"context"
	"sync"

	"github.com/imevul/hapudding/internal/config"
	"github.com/imevul/hapudding/internal/store"
)

// Flags is YAML disabled plus a store-backed runtime overlay.
type Flags struct {
	mu      sync.RWMutex
	yaml    map[string]bool
	runtime map[string]bool
}

func newFlags(cfg *config.Config) *Flags {
	f := &Flags{yaml: map[string]bool{}, runtime: map[string]bool{}}
	if cfg == nil {
		return f
	}
	for _, b := range cfg.Backends {
		f.yaml[b.Name] = b.Disabled
	}
	return f
}

func (f *Flags) load(ctx context.Context, st store.Store) {
	if f == nil || st == nil {
		return
	}
	rows, err := st.ListBackendFlags(ctx)
	if err != nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for name, disabled := range rows {
		if disabled {
			f.runtime[name] = true
		}
	}
}

func (f *Flags) Disabled(name string) bool {
	if f == nil {
		return false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.yaml[name] || f.runtime[name]
}

func (f *Flags) ConfigDisabled(name string) bool {
	if f == nil {
		return false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.yaml[name]
}

func (f *Flags) RuntimeDisabled(name string) bool {
	if f == nil {
		return false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.runtime[name]
}

func (f *Flags) SetRuntime(ctx context.Context, st store.Store, name string, disabled bool) error {
	if f == nil {
		return nil
	}
	if disabled {
		if err := st.SetBackendDisabled(ctx, name, true); err != nil {
			return err
		}
		f.mu.Lock()
		f.runtime[name] = true
		f.mu.Unlock()
		return nil
	}
	if err := st.ClearBackendFlag(ctx, name); err != nil {
		return err
	}
	f.mu.Lock()
	delete(f.runtime, name)
	f.mu.Unlock()
	return nil
}
