// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"context"
	"database/sql/driver"
	"io"

	"github.com/IgorPolyakov/ifxodbc/api"
)

type Rows struct {
	os  *ODBCStmt
	ctx context.Context
}

// newRows creates Rows bound to the query context: SQLFetch and
// SQLMoreResults observe the context through ODBCStmt.callContext.
func newRows(ctx context.Context, os *ODBCStmt) *Rows {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Rows{os: os, ctx: ctx}
}

// ctxErr reports the context error once the context is done.
func (r *Rows) ctxErr() error {
	return r.ctx.Err()
}

func (r *Rows) Columns() []string {
	names := make([]string, len(r.os.Cols))
	for i := 0; i < len(names); i++ {
		names[i] = r.os.Cols[i].Name()
	}
	return names
}

func (r *Rows) Next(dest []driver.Value) error {
	if err := r.ctxErr(); err != nil {
		return err
	}
	ret, err := r.os.callContext(r.ctx, func() api.SQLRETURN { return api.SQLFetch(r.os.h) })
	if err != nil {
		// cancelled: the cursor may still be open, Rows.Close closes it
		return err
	}
	if ret == api.SQL_NO_DATA {
		return io.EOF
	}
	if IsError(ret) {
		return NewError("SQLFetch", r.os.h)
	}
	for i := range dest {
		v, err := r.os.Cols[i].Value(r.os.h, i)
		if err == errBoundTruncated {
			// SQLGetData can only read columns after the last bound
			// one, so unbind this column and the rest, and read again.
			if err = r.os.unbindFrom(i); err != nil {
				return err
			}
			v, err = r.os.Cols[i].Value(r.os.h, i)
		}
		if err != nil {
			if e := r.ctxErr(); e != nil {
				return e
			}
			return err
		}
		dest[i] = v
	}
	return nil
}

func (r *Rows) Close() error {
	return r.os.closeByRows()
}

func (r *Rows) HasNextResultSet() bool {
	return true
}

func (r *Rows) NextResultSet() error {
	if err := r.ctxErr(); err != nil {
		return err
	}
	ret, err := r.os.callContext(r.ctx, func() api.SQLRETURN { return api.SQLMoreResults(r.os.h) })
	if err != nil {
		// cancelled: the new result set may have opened, close its cursor
		api.SQLCloseCursor(r.os.h)
		return err
	}
	if ret == api.SQL_NO_DATA {
		return io.EOF
	}
	if IsError(ret) {
		return NewError("SQLMoreResults", r.os.h)
	}

	err = r.os.bindColumnsContext(r.ctx)
	if err != nil {
		return err
	}
	return nil
}

// Implement RowsColumnTypeDatabaseTypeName interface in order to return column types.
// https://github.com/golang/go/blob/e22a14b7eb1e4a172d0c20d14a0d2433fdf20e5c/src/database/sql/driver/driver.go#L469-L477
//
// From the docs:
// RowsColumnTypeDatabaseTypeName may be implemented by Rows. It should return the
// database system type name without the length. Type names should be uppercase.
// Examples of returned types: "VARCHAR", "NVARCHAR", "VARCHAR2", "CHAR", "TEXT",
// "DECIMAL", "SMALLINT", "INT", "BIGINT", "BOOL", "[]BIGINT", "JSONB", "XML",
// "TIMESTAMP".
func (rs *Rows) ColumnTypeDatabaseTypeName(i int) string {
	return rs.os.Cols[i].DatabaseTypeName()
}
