// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
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
	// SQL_TIME_STRUCT only supports hours, minutes and seconds
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
