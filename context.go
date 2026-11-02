// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"time"

	"github.com/IgorPolyakov/ifxodbc/api"
)

// callWithContext repeats cancellation until call returns: SQLCancel sent before
// entry into the ODBC call does not cancel a later invocation. Waiting for the
// watcher also prevents cancellation from reaching the next use of the handle.
func callWithContext(ctx context.Context, call func() api.SQLRETURN, cancel func()) (api.SQLRETURN, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if ctx.Done() == nil {
		return call(), nil
	}
	finished := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		select {
		case <-finished:
			return
		case <-ctx.Done():
		}
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-finished:
				return
			default:
			}
			cancel()
			select {
			case <-finished:
				return
			case <-ticker.C:
			}
		}
	}()
	ret := call()
	close(finished)
	<-exited
	return ret, ctx.Err()
}
