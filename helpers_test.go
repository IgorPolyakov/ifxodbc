// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"database/sql"
	"testing"
)

func closeDB(t *testing.T, db *sql.DB, shouldStmtCount, ignoreIfStmtCount int) {
	err := db.Close()
	if err != nil {
		t.Fatalf("error closing DB: %v", err)
	}
	// Snapshot the counter after Close, so handles released by Close are
	// counted too; StmtCountSnapshot avoids copying the Stats mutex.
	s := db.Driver().(*Driver).Stats.StmtCountSnapshot()
	switch s {
	case shouldStmtCount:
		// all good
	case ignoreIfStmtCount:
		t.Logf("ignoring unexpected StmtCount of %v", ignoreIfStmtCount)
	default:
		t.Errorf("unexpected StmtCount: should=%v, is=%v", shouldStmtCount, s)
	}
}

func exec(t *testing.T, db *sql.DB, query string) {
	// TODO(brainman): make sure https://github.com/golang/go/issues/3678 is fixed
	//r, err := db.Exec(query, a...)
	s, err := db.Prepare(query)
	if err != nil {
		t.Fatalf("db.Prepare(%q) failed: %v", query, err)
	}
	defer s.Close()
	r, err := s.Exec()
	if err != nil {
		t.Fatalf("s.Exec(%q ...) failed: %v", query, err)
	}
	_, err = r.RowsAffected()
	if err != nil {
		t.Fatalf("r.RowsAffected(%q ...) failed: %v", query, err)
	}
}
