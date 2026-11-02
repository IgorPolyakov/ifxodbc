// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"database/sql"
	"flag"
	"fmt"
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
	stats := db.Driver().(*Driver).Stats
	return db, stats.StmtCount, nil
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

	exec(t, db, "drop table temp")
}
