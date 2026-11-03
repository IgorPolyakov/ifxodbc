// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

var (
	ifxsrv  = flag.String("ifxsrv", "server", "ifx server name")
	ifxdb   = flag.String("ifxdb", "dbname", "ifx database name")
	ifxuser = flag.String("ifxuser", "", "ifx user name")
	ifxpass = flag.String("ifxpass", "", "ifx password")
)

func ifxConnect() (db *sql.DB, stmtCount int, err error) {
	conn := fmt.Sprintf("driver=IBM INFORMIX ODBC DRIVER;server=%s;database=%s;uid=%s;pwd=%s",
		*ifxsrv, *ifxdb, *ifxuser, *ifxpass)
	db, err = sql.Open("odbc", conn)
	if err != nil {
		return nil, 0, err
	}
	return db, db.Driver().(*Driver).Stats.StmtCountSnapshot(), nil
}

func TestIFXTime(t *testing.T) {
	db, sc, err := ifxConnect()
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB(t, db, sc, sc)

	db.Exec("drop table temp")
	exec(t, db, "create table temp(id serial primary key, time datetime year to second)")
	now := time.Now()
	// The column keeps only the time of day: datetime year to second has
	// no date part, and BindValue sends an SQL_TIMESTAMP_STRUCT whose
	// zero fraction matches the column precision.
	now = time.Date(1, time.January, 1, now.Hour(), now.Minute(), now.Second(), 0, time.Local)
	_, err = db.Exec("insert into temp (time) values(?)", now)
	if err != nil {
		t.Fatal(err)
	}

	var ret time.Time
	if err := db.QueryRow("select time from temp where id = ?", 1).Scan(&ret); err != nil {
		t.Fatal(err)
	}
	if ret != now {
		t.Fatalf("unexpected return value: want=%v, is=%v", now, ret)
	}

	exec(t, db, "drop table temp")
}

func TestIFXBigint(t *testing.T) {
	db, sc, err := ifxConnect()
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB(t, db, sc, sc)

	db.Exec("drop table temp")
	// The Informix ODBC driver reports BIGINT columns as SQL_INFX_BIGINT
	// (-114), not SQL_BIGINT; reading them must still yield int64 values.
	exec(t, db, "create table temp(id int, big bigint)")
	defer exec(t, db, "drop table temp")

	const edge = int64(9223372036854775807)
	if _, err := db.Exec("insert into temp (id, big) values (1, ?)", edge); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into temp (id, big) values (2, ?)", -edge); err != nil {
		t.Fatal(err)
	}
	var got, neg int64
	if err := db.QueryRow("select big from temp where id = 1").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != edge {
		t.Errorf("bigint max: want=%d, is=%d", edge, got)
	}
	if err := db.QueryRow("select big from temp where id = 2").Scan(&neg); err != nil {
		t.Fatal(err)
	}
	if neg != -edge {
		t.Errorf("bigint min: want=%d, is=%d", -edge, neg)
	}
}

func TestIFXBeginErr(t *testing.T) {
	before := ifxStressHandles()
	dsn := fmt.Sprintf("driver=IBM INFORMIX ODBC DRIVER;server=%s;database=%s;uid=%s;pwd=%s",
		*ifxsrv, *ifxdb, *ifxuser, *ifxpass)
	conn, err := drv.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	c := conn.(*Conn)

	testBeginErr = errors.New("injected setAutoCommitAttr error")
	_, err = c.Begin()
	testBeginErr = nil
	if err == nil {
		t.Fatal("expected Begin to fail with the injected error")
	}
	if c.tx != nil {
		t.Fatal("failed Begin left the connection in transaction state")
	}
	if !c.bad {
		t.Fatal("failed Begin must mark the connection bad")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	after := ifxStressHandles()
	if before != after {
		t.Errorf("handle leak: %v -> %v", before, after)
	}
}

func TestIFXTruncatedColumns(t *testing.T) {
	db, sc, err := ifxConnect()
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB(t, db, sc, sc)

	db.Exec("drop table ifx_trunc")
	exec(t, db, "create table ifx_trunc (id int primary key, a varchar(255), b varchar(254), c lvarchar(4000))")
	defer exec(t, db, "drop table ifx_trunc")

	sp := func(s string) *string { return &s }
	cases := []struct {
		id      int
		a, b, c *string // nil means the column was not inserted
	}{
		{1, sp(strings.Repeat("x", 254)), sp(""), nil},
		{2, sp(strings.Repeat("x", 255)), sp(strings.Repeat("y", 253)), nil},
		{3, nil, nil, sp(strings.Repeat("z", 4000))},
	}
	for _, c := range cases {
		if c.id == 3 {
			continue
		}
		if _, err := db.Exec("insert into ifx_trunc (id, a, b) values (?, ?, ?)", c.id, *c.a, *c.b); err != nil {
			t.Fatalf("id=%d: %v", c.id, err)
		}
	}
	// The long value goes in as a literal: a 4000+ character parameter
	// binds as text, which this server cannot cast into lvarchar.
	if _, err := db.Exec(fmt.Sprintf("insert into ifx_trunc (id, c) values (3, '%s')", strings.Repeat("z", 4000))); err != nil {
		t.Fatal(err)
	}
	// An explicit NULL next to an empty string: both contracts differ.
	if _, err := db.Exec("insert into ifx_trunc (id, a, b) values (4, null, '')"); err != nil {
		t.Fatal(err)
	}
	cases = append(cases, struct {
		id      int
		a, b, c *string
	}{4, nil, sp(""), nil})

	rows, err := db.Query("select id, a, b, c from ifx_trunc order by id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cts, err := rows.ColumnTypes()
	if err != nil {
		t.Fatal(err)
	}
	lengths := make([]int64, len(cts))
	for i, ct := range cts {
		if n, ok := ct.Length(); ok {
			lengths[i] = n
		}
	}
	t.Logf("reported column lengths: %v", lengths)

	checked := 0
	// NULL and the empty string are distinct: a column left out of the
	// insert round-trips as NULL, an empty string parameter as "".
	check := func(id int, col string, got sql.NullString, want *string) {
		t.Helper()
		if want == nil {
			if got.Valid {
				t.Errorf("id=%d: column %s: want NULL, got %q", id, col, got.String)
			}
			return
		}
		if !got.Valid || got.String != *want {
			t.Errorf("id=%d: column %s: want %d bytes, got valid=%v %d bytes", id, col, len(*want), got.Valid, len(got.String))
		}
	}
	for rows.Next() {
		var id int
		var a, b, c sql.NullString
		if err := rows.Scan(&id, &a, &b, &c); err != nil {
			t.Fatal(err)
		}
		want := cases[id-1]
		check(id, "a", a, want.a)
		check(id, "b", b, want.b)
		check(id, "c", c, want.c)
		checked++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if checked != 4 {
		t.Fatalf("row count: want=4, got=%d", checked)
	}
}

func TestIFXDateTimeFraction(t *testing.T) {
	db, sc, err := ifxConnect()
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB(t, db, sc, sc)

	db.Exec("drop table temp")
	exec(t, db, "create table temp(id serial primary key, ts datetime year to fraction(5))")
	defer exec(t, db, "drop table temp")

	// fraction(5) keeps 10-microsecond units; the round trip goes through
	// the local zone on both write and read
	now := time.Now().Truncate(10 * time.Microsecond)
	if _, err := db.Exec("insert into temp (ts) values (?)", now); err != nil {
		t.Fatal(err)
	}
	var ret time.Time
	if err := db.QueryRow("select ts from temp where id = 1").Scan(&ret); err != nil {
		t.Fatal(err)
	}
	if diff := ret.Sub(now); diff < -20*time.Microsecond || diff > 20*time.Microsecond {
		t.Fatalf("round trip drifted by %v: want=%v, got=%v", diff, now, ret)
	}
}

func TestIFXRebindGC(t *testing.T) {
	db, sc, err := ifxConnect()
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB(t, db, sc, sc)

	db.Exec("drop table ifx_rebind")
	exec(t, db, "create table ifx_rebind (id int, s varchar(128), n decimal(10,2), f float)")
	defer exec(t, db, "drop table ifx_rebind")

	stmt, err := db.Prepare("insert into ifx_rebind values (?, ?, ?, ?)")
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()

	// Every execution rebinds the parameters, replacing the buffers the
	// driver retained; the GC churn around the binds must not disturb
	// the data the following SQLExecute reads.
	for i := 0; i < 100; i++ {
		want := fmt.Sprintf("value-%d", i)
		dec := fmt.Sprintf("%d.25", i)
		f := float64(i) + 0.5
		runtime.GC()
		if _, err := stmt.Exec(i, want, dec, f); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		runtime.GC()
	}
	var count int
	if err := db.QueryRow("select count(*) from ifx_rebind").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 100 {
		t.Fatalf("row count: want=100, got=%d", count)
	}
	var s, n string
	if err := db.QueryRow("select s, n from ifx_rebind where id = 42").Scan(&s, &n); err != nil {
		t.Fatal(err)
	}
	if s != "value-42" {
		t.Errorf("string: want=value-42, got=%v", s)
	}
	if strings.TrimSpace(n) != "42.25" {
		t.Errorf("decimal: want=42.25, got=%v", n)
	}
}

func TestIFXDecimal(t *testing.T) {
	db, sc, err := ifxConnect()
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB(t, db, sc, sc)

	db.Exec("drop table temp")
	exec(t, db, "create table temp(id int, big decimal(32,0), frac decimal(16,4))")
	defer exec(t, db, "drop table temp")

	// 2^53+1 is not representable in float64: reading DECIMAL through
	// float64 loses the last digit, the exact text must survive instead.
	const big = "9007199254740993"
	if _, err := db.Exec("insert into temp (id, big, frac) values (1, ?, ?)", big, "1.0001"); err != nil {
		t.Fatal(err)
	}
	var gotBig, gotFrac string
	if err := db.QueryRow("select big, frac from temp where id = 1").Scan(&gotBig, &gotFrac); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(gotBig) != big {
		t.Errorf("decimal(32,0): want=%v, got=%v", big, gotBig)
	}
	if strings.TrimSpace(gotFrac) != "1.0001" {
		t.Errorf("decimal(16,4): want=1.0001, got=%v", gotFrac)
	}
}

func TestIFXColumnTypes(t *testing.T) {
	db, sc, err := ifxConnect()
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB(t, db, sc, sc)

	db.Exec("drop table temp")
	exec(t, db, "create table temp(id int, name varchar(20), code char(10), ts datetime year to second)")

	rows, err := db.Query("select id, name, code, ts from temp")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	types, err := rows.ColumnTypes()
	if err != nil {
		t.Fatal(err)
	}
	for _, ct := range types {
		if ct.DatabaseTypeName() == "" {
			t.Errorf("column %q: empty database type name", ct.Name())
		}
		t.Logf("column %q: %v", ct.Name(), ct.DatabaseTypeName())
	}
	for _, test := range []struct{ col, want string }{
		{"id", "INTEGER"},
		{"name", "VARCHAR"},
		{"code", "CHAR"},
	} {
		var got string
		for _, ct := range types {
			if ct.Name() == test.col {
				got = ct.DatabaseTypeName()
				break
			}
		}
		if got == "" {
			t.Fatalf("column %q not found in result set", test.col)
		}
		if got != test.want {
			t.Errorf("column %q type name: want=%v, got=%v", test.col, test.want, got)
		}
	}

	// Informix refuses to drop a table that still has an open cursor.
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	exec(t, db, "drop table temp")
}
