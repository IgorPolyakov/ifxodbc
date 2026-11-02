// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgorPolyakov/ifxodbc/api"
)

func TestCallWithContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := callWithContext(ctx, func() api.SQLRETURN {
		t.Error("called ODBC with an already canceled context")
		return api.SQL_SUCCESS
	}, func() { t.Error("canceled an operation that never started") })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestCallWithContextCancelBeforeEntry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstCancel := make(chan struct{})
	interrupted := make(chan struct{})
	var active atomic.Bool
	var count int
	_, err := callWithContext(ctx, func() api.SQLRETURN {
		cancel()
		// Force the first cancellation to arrive before native execution.
		select {
		case <-firstCancel:
		case <-time.After(time.Second):
			t.Error("no cancellation before entry")
			return api.SQL_SUCCESS
		}
		active.Store(true)
		select {
		case <-interrupted:
		case <-time.After(time.Second):
			t.Error("cancellation before entry was lost")
		}
		return api.SQL_SUCCESS
	}, func() {
		count++
		if count == 1 {
			close(firstCancel)
		}
		if active.Load() {
			select {
			case <-interrupted:
			default:
				close(interrupted)
			}
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if count < 2 {
		t.Fatalf("want repeated cancellation, got %d calls", count)
	}
}

func TestCallWithContextWaitsForCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelStarted := make(chan struct{})
	releaseCancel := make(chan struct{})
	returned := make(chan error, 1)
	go func() {
		_, err := callWithContext(ctx, func() api.SQLRETURN {
			cancel()
			<-cancelStarted
			return api.SQL_SUCCESS
		}, func() {
			select {
			case <-cancelStarted:
			default:
				close(cancelStarted)
			}
			<-releaseCancel
		})
		returned <- err
	}()
	<-cancelStarted
	select {
	case err := <-returned:
		t.Errorf("returned before cancellation finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseCancel)
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("want context.Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("did not return after cancellation finished")
	}
}
