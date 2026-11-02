// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"database/sql/driver"
	"errors"
	"sync"

	"github.com/IgorPolyakov/ifxodbc/api"
)

type Stmt struct {
	c     *Conn
	query string
	os    *ODBCStmt
	mu    sync.Mutex
}

func (c *Conn) Prepare(query string) (driver.Stmt, error) {
	if c.bad {
		return nil, driver.ErrBadConn
	}
	os, err := c.PrepareODBCStmt(query)
	if err != nil {
		return nil, err
	}
	return &Stmt{c: c, os: os, query: query}, nil
}

func (s *Stmt) NumInput() int {
	if s.os == nil {
		return -1
	}
	return len(s.os.Parameters)
}

func (s *Stmt) Close() error {
	if s.os == nil {
		return errors.New("Stmt is already closed")
	}
	ret := s.os.closeByStmt()
	s.os = nil
	return ret
}

// reprepare replaces the statement handle after its previous result set
// was not fully consumed. It must be called with s.mu held.
func (s *Stmt) reprepare() error {
	s.os.closeByStmt()
	s.os = nil
	os, err := s.c.PrepareODBCStmt(s.query)
	if err != nil {
		return err
	}
	s.os = os
	return nil
}

func (s *Stmt) Exec(args []driver.Value) (driver.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.os == nil {
		return nil, errors.New("Stmt is closed")
	}
	if s.os.usedByRows {
		if err := s.reprepare(); err != nil {
			return nil, err
		}
	}
	err := s.os.Exec(args, s.c)
	if err != nil {
		return nil, err
	}
	return s.sumRowCount()
}

// ExecContext implements driver.StmtExecContext. Like Conn.QueryContext,
// it honours the context: when the context is cancelled, it cancels the
// statement, waits for SQLExecute to return and reports the context error.
func (s *Stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	dargs, err := namedValueToValue(args)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.os == nil {
		return nil, errors.New("Stmt is closed")
	}
	if s.os.usedByRows {
		if err := s.reprepare(); err != nil {
			return nil, err
		}
	}

	if err := s.os.execContext(ctx, dargs, s.c); err != nil {
		return nil, err
	}
	return s.sumRowCountContext(ctx)
}

// sumRowCount sums row counts of all result sets of the executed statement.
func (s *Stmt) sumRowCount() (driver.Result, error) {
	return s.sumRowCountContext(context.Background())
}

func (s *Stmt) sumRowCountContext(ctx context.Context) (driver.Result, error) {
	var sumRowCount int64
	for {
		var c api.SQLLEN
		ret := api.SQLRowCount(s.os.h, &c)
		if IsError(ret) {
			return nil, NewError("SQLRowCount", s.os.h)
		}
		sumRowCount += int64(c)
		ret, err := s.os.callContext(ctx, func() api.SQLRETURN { return api.SQLMoreResults(s.os.h) })
		if err != nil {
			api.SQLCloseCursor(s.os.h)
			return nil, err
		}
		if ret == api.SQL_NO_DATA {
			break
		}
		if IsError(ret) {
			return nil, NewError("SQLMoreResults", s.os.h)
		}
	}
	return &Result{rowCount: sumRowCount}, nil
}

func (s *Stmt) Query(args []driver.Value) (driver.Rows, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.os == nil {
		return nil, errors.New("Stmt is closed")
	}
	if s.os.usedByRows {
		if err := s.reprepare(); err != nil {
			return nil, err
		}
	}
	err := s.os.Exec(args, s.c)
	if err != nil {
		return nil, err
	}
	err = s.os.BindColumns()
	if err != nil {
		return nil, err
	}
	s.os.usedByRows = true // now both Stmt and Rows refer to it
	return &Rows{os: s.os}, nil
}

// QueryContext implements driver.StmtQueryContext. Like Conn.QueryContext,
// it honours the context: when the context is cancelled, it cancels the
// statement, waits for the cancel watcher and reports the context error.
func (s *Stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	dargs, err := namedValueToValue(args)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.os == nil {
		return nil, errors.New("Stmt is closed")
	}
	if s.os.usedByRows {
		if err := s.reprepare(); err != nil {
			return nil, err
		}
	}

	if err := s.os.execContext(ctx, dargs, s.c); err != nil {
		return nil, err
	}
	if err := s.os.BindColumns(); err != nil {
		return nil, err
	}
	s.os.usedByRows = true // now both Stmt and Rows refer to it
	return &Rows{os: s.os}, nil
}
