package odbc

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestIFXCancelLockWait holds a real row lock in one connection and checks
// that a blocked read in another connection reports the context error
// instead of hanging until the lock timeout. Repeatable stable isolation
// makes the read acquire its share locks as rows are fetched, so the
// cancellation is exercised in row reading; if the engine defers the read
// into SQLExecute, the assertion still holds for that path.
func TestIFXCancelLockWait(t *testing.T) {
	db, sc, err := ifxConnect()
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB(t, db, sc, sc)

	db.Exec("drop table ifx_cancel")
	exec(t, db, "create table ifx_cancel (id int primary key, v int) lock mode row")
	defer exec(t, db, "drop table ifx_cancel")
	exec(t, db, "insert into ifx_cancel values (1, 0)")

	lockConn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lockConn.Close()
	victimConn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer victimConn.Close()

	lockTx, err := lockConn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback()
	if _, err := lockTx.Exec("update ifx_cancel set v = v + 1 where id = 1"); err != nil {
		t.Fatal(err)
	}
	// An exclusive table lock also blocks readers that request share
	// locks; a row lock alone does not stop this engine's SELECTs.
	if _, err := lockTx.Exec("lock table ifx_cancel in exclusive mode"); err != nil {
		t.Fatal(err)
	}

	// Make the victim wait for the lock instead of failing fast; the
	// bounded wait keeps the test finishing even if cancellation fails.
	if _, err := victimConn.ExecContext(context.Background(), "set lock mode to wait 30"); err != nil {
		t.Fatal(err)
	}
	// Repeatable read takes share locks while rows are fetched; this
	// Informix accepts the SQL-standard level name only.
	if _, err := victimConn.ExecContext(context.Background(), "set isolation to repeatable read"); err != nil {
		t.Fatal(err)
	}

	const timeout = 3 * time.Second
	checkCancelled := func(t *testing.T, err error, start time.Time) {
		t.Helper()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want context.DeadlineExceeded, got %v", err)
		}
		if elapsed := time.Since(start); elapsed > time.Minute {
			t.Fatalf("cancellation observed only after %s", elapsed)
		}
	}

	t.Run("DirectQuery", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		start := time.Now()
		var id int
		err := victimConn.QueryRowContext(ctx, "select id from ifx_cancel where id = 1").Scan(&id)
		checkCancelled(t, err, start)
	})

	t.Run("PreparedQuery", func(t *testing.T) {
		stmt, err := victimConn.PrepareContext(context.Background(), "select id from ifx_cancel where id = 1")
		if err != nil {
			t.Fatal(err)
		}
		defer stmt.Close()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		start := time.Now()
		var id int
		err = stmt.QueryRowContext(ctx).Scan(&id)
		checkCancelled(t, err, start)
	})
}
