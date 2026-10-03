// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-FileCopyrightText: 2016 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSlowStartBatch(t *testing.T) {
	fakeErr := fmt.Errorf("fake error")
	callCnt := 0
	callLimit := 0
	var lock sync.Mutex
	fn := func(idx int) error {
		lock.Lock()
		defer lock.Unlock()
		callCnt++
		if callCnt > callLimit {
			return fakeErr
		}
		return nil
	}

	tests := []struct {
		name              string
		count             int
		initialBatchSize  int
		callLimit         int
		fn                func(int) error
		expectedSuccesses int
		expectedErr       error
		expectedCallCnt   int
	}{
		{
			name:              "callLimit = 0 (all fail)",
			count:             10,
			initialBatchSize:  1,
			callLimit:         0,
			fn:                fn,
			expectedSuccesses: 0,
			expectedErr:       fakeErr,
			expectedCallCnt:   1, // 1(first batch): function will be called at least once
		},
		{
			name:              "callLimit = count (all succeed)",
			count:             10,
			initialBatchSize:  1,
			callLimit:         10,
			fn:                fn,
			expectedSuccesses: 10,
			expectedErr:       nil,
			expectedCallCnt:   10, // 1(first batch) + 2(2nd batch) + 4(3rd batch) + 3(4th batch) = 10
		},
		{
			name:              "callLimit < count (some succeed)",
			count:             10,
			initialBatchSize:  1,
			callLimit:         5,
			fn:                fn,
			expectedSuccesses: 5,
			expectedErr:       fakeErr,
			expectedCallCnt:   7, // 1(first batch) + 2(2nd batch) + 4(3rd batch) = 7
		},
		{
			name:              "initialBatchSize > 1 (all succeed)",
			count:             10,
			initialBatchSize:  4,
			callLimit:         10,
			fn:                fn,
			expectedSuccesses: 10,
			expectedErr:       nil,
			expectedCallCnt:   10, // 4(first batch) + 6(2nd batch, capped by remaining) = 10
		},
		{
			name:             "initialBatchSize > 1 (first batch fails)",
			count:            10,
			initialBatchSize: 4,
			callLimit:        2,
			fn:               fn,
			// A larger initial batch trades error containment for fewer
			// barriers: 2 calls fail here instead of the 1 that
			// initialBatchSize=1 would allow.
			expectedSuccesses: 2,
			expectedErr:       fakeErr,
			expectedCallCnt:   4, // 4(first batch), remaining batches skipped
		},
		{
			name:             "initialBatchSize < 1 (silent no-op)",
			count:            10,
			initialBatchSize: 0,
			callLimit:        10,
			fn:               fn,
			// Documents why callers must clamp to at least 1: the batch loop
			// never runs and success is reported without doing any work.
			expectedSuccesses: 0,
			expectedErr:       nil,
			expectedCallCnt:   0,
		},
	}

	for _, test := range tests {
		callCnt = 0
		callLimit = test.callLimit
		successes, err := SlowStartBatch(test.count, test.initialBatchSize, test.fn)

		require.Equal(t, test.expectedSuccesses, successes, "%s: unexpected processed batch size", test.name)
		require.ErrorIs(t, err, test.expectedErr, "%s: unexpected error", test.name)
		// verify that slowStartBatch stops trying more calls after a batch fails
		require.Equal(t, test.expectedCallCnt, callCnt, "%s: slowStartBatch() still tries calls after a batch fails", test.name)
	}
}

// TestSlowStartBatchRecoversPanic asserts that a panic escaping fn is turned
// into a batch error. The calls run on bare goroutines, so without the recover
// the panic would terminate the whole process rather than fail this batch, and
// this test would take the test binary down with it.
func TestSlowStartBatchRecoversPanic(t *testing.T) {
	var lock sync.Mutex
	callCnt := 0
	fn := func(idx int) error {
		lock.Lock()
		callCnt++
		lock.Unlock()
		if idx == 2 {
			panic("boom")
		}
		return nil
	}

	successes, err := SlowStartBatch(10, 4, fn)

	require.Error(t, err)
	require.Contains(t, err.Error(), "boom")
	require.Equal(t, 3, successes, "the three non-panicking calls in the batch should count as successes")
	require.Equal(t, 4, callCnt, "remaining batches should be skipped after the panic")
}
