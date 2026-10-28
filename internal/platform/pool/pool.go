// Package pool runs work with bounded concurrency.
package pool

import "sync"

// Run calls fn for every item with at most limit goroutines and returns results in input order.
func Run[T, R any](items []T, limit int, fn func(T) R) []R {
	out := make([]R, len(items))
	if len(items) == 0 {
		return out
	}
	limit = max(1, min(limit, len(items)))
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i, item := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = fn(item)
		}()
	}
	wg.Wait()
	return out
}
