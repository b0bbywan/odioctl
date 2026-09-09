package web

import (
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

// removedFrom signals each removal of name from dir, coalesced, until stop.
// Watch errors go to logf and the watch goes on.
func removedFrom(dir, name string, logf func(string, ...any)) (gone <-chan struct{}, stop func(), err error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil, err
	}
	if err := w.Add(dir); err != nil {
		_ = w.Close()
		return nil, nil, err
	}
	ch := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if filepath.Base(ev.Name) == name && ev.Has(fsnotify.Remove|fsnotify.Rename) {
					select {
					case ch <- struct{}{}:
					default:
					}
				}
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				logf("watching %s: %v", dir, err)
			}
		}
	}()
	return ch, func() { _ = w.Close() }, nil
}
