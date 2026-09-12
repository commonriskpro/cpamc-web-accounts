package main

import (
	"context"
	"sync"
)

// pluginLifecycle keeps asynchronous stream workers attached to the native
// host lifetime. The host invalidates its callback context immediately after
// plugin.shutdown returns, so shutdown must first quiesce new calls, cancel
// workers, close their upstream streams, and wait for every callback to leave.
type pluginLifecycle struct {
	mu       sync.Mutex
	draining bool
	calls    sync.WaitGroup
	workers  sync.WaitGroup
	active   map[*streamWorker]struct{}
}

type streamWorker struct {
	mu         sync.Mutex
	cancel     context.CancelFunc
	upstreamID string
}

var lifecycle = pluginLifecycle{active: make(map[*streamWorker]struct{})}

var pluginState struct {
	mu           sync.Mutex
	active       bool
	shuttingDown bool
}

// beginPluginInit reserves the one active ABI instance. A duplicate init is
// rejected before touching the stored host pointer; a new init is allowed only
// after the previous shutdown has completed.
func beginPluginInit() bool {
	pluginState.mu.Lock()
	defer pluginState.mu.Unlock()
	if pluginState.active || pluginState.shuttingDown {
		return false
	}
	pluginState.active = true
	return true
}

func abortPluginInit() {
	pluginState.mu.Lock()
	pluginState.active = false
	pluginState.mu.Unlock()
}

func beginPluginShutdown() bool {
	pluginState.mu.Lock()
	defer pluginState.mu.Unlock()
	if !pluginState.active || pluginState.shuttingDown {
		return false
	}
	pluginState.active = false
	pluginState.shuttingDown = true
	return true
}

func finishPluginShutdown() {
	pluginState.mu.Lock()
	pluginState.shuttingDown = false
	pluginState.mu.Unlock()
}

func beginPluginCall(method string) bool {
	pluginState.mu.Lock()
	defer pluginState.mu.Unlock()
	if !pluginState.active || pluginState.shuttingDown {
		return false
	}
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if lifecycle.draining && method != methodPluginQuiesce && method != methodReconfigure {
		return false
	}
	lifecycle.calls.Add(1)
	return true
}

func endPluginCall() { lifecycle.calls.Done() }

func beginStreamWorker() (*streamWorker, context.Context, bool) {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if lifecycle.draining {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &streamWorker{cancel: cancel}
	lifecycle.active[w] = struct{}{}
	lifecycle.workers.Add(1)
	return w, ctx, true
}

func (w *streamWorker) setUpstream(id string) bool {
	if w == nil {
		return false
	}
	lifecycle.mu.Lock()
	draining := lifecycle.draining
	lifecycle.mu.Unlock()
	w.mu.Lock()
	w.upstreamID = id
	w.mu.Unlock()
	return !draining
}

func (w *streamWorker) closeUpstream() {
	if w == nil {
		return
	}
	w.mu.Lock()
	id := w.upstreamID
	w.upstreamID = ""
	w.mu.Unlock()
	if id != "" {
		closeHTTPStream(id)
	}
}

func endStreamWorker(w *streamWorker) {
	if w == nil {
		return
	}
	w.closeUpstream()
	w.cancel()
	lifecycle.mu.Lock()
	delete(lifecycle.active, w)
	lifecycle.mu.Unlock()
	lifecycle.workers.Done()
}

func quiescePlugin() {
	lifecycle.mu.Lock()
	lifecycle.draining = true
	lifecycle.mu.Unlock()
}

// reopenPluginLifecycle is the rollback path for a quiesce that did not lead
// to shutdown. It holds the state lock while reopening so shutdown cannot race
// this transition and make a drained plugin accept new calls again.
func reopenPluginLifecycle() bool {
	pluginState.mu.Lock()
	defer pluginState.mu.Unlock()
	if !pluginState.active || pluginState.shuttingDown {
		return false
	}
	lifecycle.mu.Lock()
	lifecycle.draining = false
	lifecycle.mu.Unlock()
	return true
}

func shutdownPlugin() {
	quiescePlugin()

	// Close upstream streams outside the lifecycle mutex: the host callback can
	// block briefly while releasing a stream and must not prevent worker exit.
	lifecycle.mu.Lock()
	workers := make([]*streamWorker, 0, len(lifecycle.active))
	for w := range lifecycle.active {
		workers = append(workers, w)
		w.cancel()
	}
	lifecycle.mu.Unlock()
	for _, w := range workers {
		w.closeUpstream()
	}
	lifecycle.workers.Wait()
	lifecycle.calls.Wait()
}

func resetPluginLifecycle() {
	lifecycle.mu.Lock()
	lifecycle.draining = false
	lifecycle.mu.Unlock()
}
