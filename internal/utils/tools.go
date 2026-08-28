// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-FileCopyrightText: 2016 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"fmt"
	"runtime/debug"
	"sync"

	kubecontroller "k8s.io/kubernetes/pkg/controller"
	"k8s.io/utils/integer"
)

const (
	SlowStartInitialBatchSize = kubecontroller.SlowStartInitialBatchSize
)

// SlowStartBatch tries to call the provided function a total of 'count' times,
// starting slow to check for errors, then speeding up if calls succeed.
//
// It groups the calls into batches, starting with a group of initialBatchSize.
// Within each batch, it may call the function multiple times concurrently with its index.
//
// If a whole batch succeeds, the next batch may get exponentially larger.
// If there are any failures in a batch, all remaining batches are skipped
// after waiting for the current batch to complete. A panic escaping the
// function is recovered and treated as such a failure.
//
// It returns the number of successful calls to the function.
func SlowStartBatch(count, initialBatchSize int, fn func(index int) error) (int, error) {
	remaining := count
	successes := 0
	index := 0
	for batchSize := integer.IntMin(remaining, initialBatchSize); batchSize > 0; batchSize = integer.IntMin(2*batchSize, remaining) {
		errCh := make(chan error, batchSize)
		var wg sync.WaitGroup
		wg.Add(batchSize)
		for range batchSize {
			go func(idx int) {
				defer wg.Done()
				// controller-runtime's RecoverPanic only wraps Reconcile on its
				// own goroutine, so a panic escaping fn here would terminate the
				// manager process rather than fail a single call. Report it as a
				// batch error so the remaining batches are skipped instead.
				defer func() {
					if r := recover(); r != nil {
						errCh <- fmt.Errorf("panic: %v [recovered]\n%s", r, debug.Stack())
					}
				}()
				if err := fn(idx); err != nil {
					errCh <- err
				}
			}(index)
			index++
		}
		wg.Wait()
		curSuccesses := batchSize - len(errCh)
		successes += curSuccesses
		if len(errCh) > 0 {
			return successes, <-errCh
		}
		remaining -= batchSize
	}
	return successes, nil
}
