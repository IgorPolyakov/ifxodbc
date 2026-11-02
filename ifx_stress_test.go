package odbc

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var (
	ifxStress         = flag.Bool("ifxstress", false, "enable live Informix stress tests")
	ifxStressDuration = flag.Duration("ifxstressduration", 2*time.Minute, "stress duration")
	ifxStressWorkers  = flag.Int("ifxstressworkers", 8, "stress workers")
	ifxStressSeed     = flag.Int64("ifxstressseed", 1, "stress random seed")
)

const ifxStressOperations = 12
const ifxStressQuery = "select 1 from sysmaster:sysdual"

type ifxStressSession struct {
	conn   *sql.Conn
	insert *sql.Stmt
}

// Each worker owns a connection and a session-local table.
func newIFXStressSession(t testing.TB, db *sql.DB) *ifxStressSession {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := conn.ExecContext(context.Background(), "create temp table ifx_stress (id integer) with no log"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := conn.ExecContext(context.Background(), "drop table ifx_stress"); err != nil {
			t.Error(err)
		}
	})
	stmt, err := conn.PrepareContext(context.Background(), "insert into ifx_stress values (?)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stmt.Close(); err != nil {
			t.Error(err)
		}
	})
	return &ifxStressSession{conn: conn, insert: stmt}
}

// Read the counters under their own lock, without copying the embedded mutex.
func ifxStressHandles() [3]int {
	drv.Stats.mu.Lock()
	defer drv.Stats.mu.Unlock()
	return [3]int{drv.EnvCount, drv.ConnCount, drv.StmtCount}
}

func openIFXStressDB(t testing.TB, workers int) *sql.DB {
	t.Helper()
	before := ifxStressHandles()
	db, _, err := ifxConnect()
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(workers*2 + 2)
	db.SetMaxIdleConns(workers*2 + 2)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
		after := ifxStressHandles()
		if after != before {
			t.Errorf("handle leak: env/conn/stmt %v -> %v", before, after)
		}
	})
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	return db
}

func (s *ifxStressSession) run(db *sql.DB, operation byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var value int
	switch operation % ifxStressOperations {
	case 0: // Direct query and value verification.
		if err := db.QueryRowContext(ctx, ifxStressQuery).Scan(&value); err != nil {
			return err
		}
		if value != 1 {
			return fmt.Errorf("query value: want=1, got=%d", value)
		}
	case 1: // Early Rows.Close and column metadata.
		rows, err := db.QueryContext(ctx, "select tabid from systables")
		if err != nil {
			return err
		}
		defer rows.Close()
		columns, err := rows.ColumnTypes()
		if err != nil {
			return err
		}
		if len(columns) != 1 || columns[0].DatabaseTypeName() != "INTEGER" {
			return fmt.Errorf("unexpected column metadata: %v", columns)
		}
		if !rows.Next() {
			return fmt.Errorf("no first row: %v", rows.Err())
		}
		if err := rows.Scan(&value); err != nil {
			return err
		}
		return rows.Close()
	case 2: // Prepare, execute and release a statement.
		stmt, err := db.PrepareContext(ctx, ifxStressQuery)
		if err != nil {
			return err
		}
		defer stmt.Close()
		if err := stmt.QueryRowContext(ctx).Scan(&value); err != nil {
			return err
		}
		if value != 1 {
			return fmt.Errorf("prepared value: want=1, got=%d", value)
		}
		return stmt.Close()
	case 3: // Reuse a prepared insert, check scalar binding and RowsAffected.
		result, err := s.insert.ExecContext(ctx, 7)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("insert affected %d rows, want 1", count)
		}
		if err := s.conn.QueryRowContext(ctx, "select id from ifx_stress where id = ?", 7).Scan(&value); err != nil {
			return err
		}
		if value != 7 {
			return fmt.Errorf("bound value: want=7, got=%d", value)
		}
		_, err = s.conn.ExecContext(ctx, "delete from ifx_stress")
		return err
	case 4, 5: // Transactions on the pinned connection.
		tx, err := s.conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := tx.QueryRowContext(ctx, ifxStressQuery).Scan(&value); err != nil {
			return err
		}
		if value != 1 {
			return fmt.Errorf("transaction value: want=1, got=%d", value)
		}
		if operation%ifxStressOperations == 4 {
			return tx.Commit()
		}
		return tx.Rollback()
	case 6: // Pre-canceled query.
		cancel()
		err := db.QueryRowContext(ctx, ifxStressQuery).Scan(&value)
		if !errors.Is(err, context.Canceled) {
			return fmt.Errorf("canceled query: %v", err)
		}
	case 7: // Execution error followed by successful reuse of the same connection.
		if _, err := s.conn.ExecContext(ctx, "insert into ifx_stress (missing_column) values (1)"); err == nil {
			return fmt.Errorf("invalid SQL succeeded")
		}
		if err := s.conn.QueryRowContext(ctx, ifxStressQuery).Scan(&value); err != nil {
			return err
		}
		if value != 1 {
			return fmt.Errorf("recovery value: want=1, got=%d", value)
		}
	case 8: // Drain rows to EOF and check the terminal error.
		rows, err := db.QueryContext(ctx, "select tabid from systables")
		if err != nil {
			return err
		}
		defer rows.Close()
		count := 0
		for rows.Next() {
			if err := rows.Scan(&value); err != nil {
				return err
			}
			count++
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("empty catalog")
		}
		return rows.Close()
	case 9: // Open and close a physical connection outside the shared pool.
		conn, err := drv.Open(fmt.Sprintf("driver=IBM INFORMIX ODBC DRIVER;server=%s;database=%s;uid=%s;pwd=%s", *ifxsrv, *ifxdb, *ifxuser, *ifxpass))
		if err != nil {
			return err
		}
		return conn.Close()
	case 10: // Empty result set.
		err := s.conn.QueryRowContext(ctx, "select id from ifx_stress where id = ?", -1).Scan(&value)
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("empty result: %v", err)
		}
	case 11: // Pool checkout and return.
		conn, err := db.Conn(ctx)
		if err != nil {
			return err
		}
		defer conn.Close()
		if err := conn.QueryRowContext(ctx, ifxStressQuery).Scan(&value); err != nil {
			return err
		}
		if value != 1 {
			return fmt.Errorf("pool value: want=1, got=%d", value)
		}
		return conn.Close()
	}
	return nil
}

func TestIFXStress(t *testing.T) {
	if !*ifxStress {
		t.Skip("set -ifxstress to run live stress tests")
	}
	if *ifxStressWorkers < 1 || *ifxStressWorkers > 64 || *ifxStressDuration <= 0 {
		t.Fatal("expected 1..64 workers and a positive duration")
	}
	db := openIFXStressDB(t, *ifxStressWorkers)
	sessions := make([]*ifxStressSession, *ifxStressWorkers)
	for i := range sessions {
		sessions[i] = newIFXStressSession(t, db)
		for op := byte(0); op < ifxStressOperations; op++ {
			if err := sessions[i].run(db, op); err != nil {
				t.Fatalf("warmup worker=%d operation=%d: %v", i, op, err)
			}
		}
	}
	before := ifxStressHandles()
	ctx, stop := context.WithTimeout(context.Background(), *ifxStressDuration)
	defer stop()
	var count atomic.Uint64
	var group sync.WaitGroup
	failures := make(chan error, len(sessions))
	started := time.Now()
	for worker, session := range sessions {
		group.Add(1)
		go func() {
			defer group.Done()
			rng := rand.New(rand.NewSource(*ifxStressSeed + int64(worker)))
			for iteration := 0; ctx.Err() == nil; iteration++ {
				op := byte(rng.Intn(ifxStressOperations))
				// Let each operation finish before ending the load so real errors
				// at the duration boundary are not discarded as cancellation.
				if err := session.run(db, op); err != nil {
					failures <- fmt.Errorf("seed=%d worker=%d iteration=%d operation=%d: %w", *ifxStressSeed, worker, iteration, op, err)
					stop()
					return
				}
				count.Add(1)
			}
		}()
	}
	done := make(chan struct{})
	go func() { group.Wait(); close(done) }()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
loop:
	for {
		select {
		case <-ticker.C:
			t.Logf("progress: operations=%d elapsed=%s", count.Load(), time.Since(started).Round(time.Second))
		case <-done:
			break loop
		}
	}
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	after := ifxStressHandles()
	if before[0] != after[0] || before[2] != after[2] {
		t.Errorf("handle leak during load: %v -> %v", before, after)
	}
	if count.Load() == 0 {
		t.Error("no operations completed")
	}
	t.Logf("seed=%d workers=%d operations=%d elapsed=%s handles=%v -> %v", *ifxStressSeed, *ifxStressWorkers, count.Load(), time.Since(started), before, after)
}

func FuzzIFXSequence(f *testing.F) {
	if !*ifxStress {
		f.Skip("set -ifxstress to fuzz live Informix")
	}
	sequence := make([]byte, ifxStressOperations)
	for i := range sequence {
		sequence[i] = byte(i)
	}
	f.Add(sequence)
	f.Add([]byte{3, 7, 3, 4, 5, 6, 8})
	db := openIFXStressDB(f, 1)
	f.Fuzz(func(t *testing.T, sequence []byte) {
		if len(sequence) == 0 || len(sequence) > 32 {
			t.Skip()
		}
		session := newIFXStressSession(t, db)
		for i, op := range sequence {
			if err := session.run(db, op); err != nil {
				t.Fatalf("index=%d operation=%d sequence=%x: %v", i, op%ifxStressOperations, sequence, err)
			}
		}
	})
}
