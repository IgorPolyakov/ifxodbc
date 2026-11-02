// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestIFXCancelBlocked(t *testing.T) {
	for _, mode := range []string{"exec", "prepared_exec", "query", "prepared_query"} {
		t.Run(mode, func(t *testing.T) {
			db := openIFXStressDB(t, 2)
			table := fmt.Sprintf("ifx_cancel_%x", time.Now().UnixNano())
			exec(t, db, "create table "+table+" (id integer primary key, n integer) lock mode row")
			t.Cleanup(func() { exec(t, db, "drop table "+table) })
			exec(t, db, "insert into "+table+" values (1, 0)")
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := conn.Close(); err != nil {
					t.Error(err)
				}
			})
			if _, err := conn.ExecContext(context.Background(), "set lock mode to wait 10"); err != nil {
				t.Fatal(err)
			}
			query := "update " + table + " set n = n + 1 where id = 1"
			stmt, err := conn.PrepareContext(context.Background(), query)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := stmt.Close(); err != nil {
					t.Error(err)
				}
			})

			locker, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer locker.Rollback()
			if _, err := locker.Exec("update " + table + " set n = 10 where id = 1"); err != nil {
				t.Fatal(err)
			}
			// Release the lock even if cancellation is broken, so the test can
			// report the regression instead of waiting for the global timeout.
			released := make(chan error, 1)
			watchdog := time.AfterFunc(3*time.Second, func() { released <- locker.Rollback() })
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			started := time.Now()
			switch mode {
			case "exec":
				_, err = conn.ExecContext(ctx, query)
			case "prepared_exec":
				_, err = stmt.ExecContext(ctx)
			case "query", "prepared_query":
				// DML makes SQLExecute wait on the lock, independently of whether
				// a driver's SELECT defers reading until SQLFetch. If cancellation
				// works, Query never reaches the no-result-set check in BindColumns.
				var rows *sql.Rows
				if mode == "query" {
					rows, err = conn.QueryContext(ctx, query)
				} else {
					rows, err = stmt.QueryContext(ctx)
				}
				if rows != nil {
					rows.Close()
				}
			}
			elapsed := time.Since(started)
			if watchdog.Stop() {
				if err := locker.Rollback(); err != nil {
					t.Error(err)
				}
			} else if err := <-released; err != nil {
				t.Error(err)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("want context.DeadlineExceeded, got %v", err)
			}
			if elapsed > 2*time.Second {
				t.Errorf("cancellation took %s, want less than 2s", elapsed)
			}
			var n int
			if err := conn.QueryRowContext(context.Background(), "select n from "+table+" where id = 1").Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Errorf("canceled update changed data: want=0, got=%d", n)
			}
			// Reuse the same prepared handle after cancellation; no watcher may
			// survive and cancel this execution.
			if _, err := stmt.ExecContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := conn.QueryRowContext(context.Background(), "select n from "+table+" where id = 1").Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Errorf("statement reuse: want=1, got=%d", n)
			}
			t.Logf("canceled blocked %s in %s; data unchanged, statement reused", mode, elapsed)
		})
	}
}
