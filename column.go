// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"github.com/IgorPolyakov/ifxodbc/api"
)

type BufferLen api.SQLLEN

func (l *BufferLen) IsNull() bool {
	return *l == api.SQL_NULL_DATA
}

func (l *BufferLen) GetData(h api.SQLHSTMT, idx int, ctype api.SQLSMALLINT, buf []byte) api.SQLRETURN {
	return api.SQLGetData(h, api.SQLUSMALLINT(idx+1), ctype,
		api.SQLPOINTER(unsafe.Pointer(&buf[0])), api.SQLLEN(len(buf)),
		(*api.SQLLEN)(l))
}

func (l *BufferLen) Bind(h api.SQLHSTMT, idx int, ctype api.SQLSMALLINT, buf []byte) api.SQLRETURN {
	return api.SQLBindCol(h, api.SQLUSMALLINT(idx+1), ctype,
		api.SQLPOINTER(unsafe.Pointer(&buf[0])), api.SQLLEN(len(buf)),
		(*api.SQLLEN)(l))
}

// Column provides access to row columns.
type Column interface {
	Name() string
	DatabaseTypeName() string
	Bind(h api.SQLHSTMT, idx int) (bool, error)
	Value(h api.SQLHSTMT, idx int) (driver.Value, error)
}

func describeColumn(h api.SQLHSTMT, idx int, namebuf []uint16) (namelen int, sqltype api.SQLSMALLINT, size api.SQLULEN, ret api.SQLRETURN) {
	var l, decimal, nullable api.SQLSMALLINT
	ret = api.SQLDescribeCol(h, api.SQLUSMALLINT(idx+1),
		(*api.SQLWCHAR)(unsafe.Pointer(&namebuf[0])),
		api.SQLSMALLINT(len(namebuf)), &l,
		&sqltype, &size, &decimal, &nullable)
	return int(l), sqltype, size, ret
}

// errDescribeCol is returned by readColumnName when SQLDescribeCol itself
// fails; the caller is expected to replace it with real diagnostics.
var errDescribeCol = errors.New("SQLDescribeCol failed")

type describeColFn func(namebuf []uint16) (namelen int, ret api.SQLRETURN)

// readColumnName reads the column name via describe. SQLDescribeCol
// reports the name length without the terminating NUL and truncates the
// name to fit the buffer, so the buffer must have room for namelen+1
// elements once the name is known to be longer than the first guess.
func readColumnName(describe describeColFn) (string, error) {
	namebuf := make([]uint16, 150)
	namelen, ret := describe(namebuf)
	if ret == api.SQL_SUCCESS_WITH_INFO && namelen >= len(namebuf) {
		// try again with bigger buffer
		namebuf = make([]uint16, namelen+1)
		namelen, ret = describe(namebuf)
	}
	if IsError(ret) {
		return "", errDescribeCol
	}
	if namelen >= len(namebuf) {
		// still complaining about buffer size
		return "", errors.New("Failed to allocate column name buffer")
	}
	return api.UTF16ToString(namebuf[:namelen]), nil
}

// TODO(brainman): did not check for MS SQL timestamp

func NewColumn(h api.SQLHSTMT, idx int) (Column, error) {
	var sqltype api.SQLSMALLINT
	var size api.SQLULEN
	describe := func(namebuf []uint16) (int, api.SQLRETURN) {
		namelen, st, sz, ret := describeColumn(h, idx, namebuf)
		sqltype, size = st, sz
		return namelen, ret
	}
	name, err := readColumnName(describe)
	if err != nil {
		if err == errDescribeCol {
			return nil, NewError("SQLDescribeCol", h)
		}
		return nil, err
	}
	b := &BaseColumn{
		name:    name,
		SQLType: sqltype,
	}
	switch sqltype {
	case api.SQL_BIT:
		return NewBindableColumn(b, api.SQL_C_BIT, 1), nil
	case api.SQL_TINYINT, api.SQL_SMALLINT, api.SQL_INTEGER:
		return NewBindableColumn(b, api.SQL_C_LONG, 4), nil
	case api.SQL_BIGINT:
		return NewBindableColumn(b, api.SQL_C_SBIGINT, 8), nil
	// NUMERIC and DECIMAL are exact numeric types: converting them through
	// float64 can lose precision for values with more than 15-17
	// significant digits, so they are read as decimal text instead and
	// keep their exact value and scale in the returned string.
	// The buffer is the column precision plus room for a sign, a decimal
	// point and the NUL terminator.
	case api.SQL_NUMERIC, api.SQL_DECIMAL:
		return NewVariableWidthColumn(b, api.SQL_C_CHAR, size+2)
	case api.SQL_FLOAT, api.SQL_REAL, api.SQL_DOUBLE:
		return NewBindableColumn(b, api.SQL_C_DOUBLE, 8), nil
	case api.SQL_TYPE_TIMESTAMP:
		var v api.SQL_TIMESTAMP_STRUCT
		return NewBindableColumn(b, api.SQL_C_TYPE_TIMESTAMP, int(unsafe.Sizeof(v))), nil
	case api.SQL_TYPE_DATE:
		var v api.SQL_DATE_STRUCT
		return NewBindableColumn(b, api.SQL_C_DATE, int(unsafe.Sizeof(v))), nil
	case api.SQL_TYPE_TIME:
		var v api.SQL_TIME_STRUCT
		return NewBindableColumn(b, api.SQL_C_TIME, int(unsafe.Sizeof(v))), nil
	case api.SQL_SS_TIME2:
		var v api.SQL_SS_TIME2_STRUCT
		return NewBindableColumn(b, api.SQL_C_BINARY, int(unsafe.Sizeof(v))), nil
	case api.SQL_GUID:
		var v api.SQLGUID
		return NewBindableColumn(b, api.SQL_C_GUID, int(unsafe.Sizeof(v))), nil
	case api.SQL_CHAR, api.SQL_VARCHAR:
		return NewVariableWidthColumn(b, api.SQL_C_CHAR, size)
	case api.SQL_WCHAR, api.SQL_WVARCHAR:
		return NewVariableWidthColumn(b, api.SQL_C_WCHAR, size)
	case api.SQL_BINARY, api.SQL_VARBINARY:
		return NewVariableWidthColumn(b, api.SQL_C_BINARY, size)
	case api.SQL_LONGVARCHAR:
		return NewVariableWidthColumn(b, api.SQL_C_CHAR, 0)
	case api.SQL_WLONGVARCHAR, api.SQL_SS_XML:
		return NewVariableWidthColumn(b, api.SQL_C_WCHAR, 0)
	case api.SQL_LONGVARBINARY:
		return NewVariableWidthColumn(b, api.SQL_C_BINARY, 0)
	default:
		return nil, fmt.Errorf("unsupported column type %d", sqltype)
	}
}

// BaseColumn implements common column functionality.
type BaseColumn struct {
	name    string
	SQLType api.SQLSMALLINT
	CType   api.SQLSMALLINT
}

func (c *BaseColumn) Name() string {
	return c.name
}

func (c *BaseColumn) DatabaseTypeName() string {
	switch c.SQLType {
	case api.SQL_CHAR:
		return "CHAR"
	case api.SQL_NUMERIC:
		return "NUMERIC"
	case api.SQL_DECIMAL:
		return "DECIMAL"
	case api.SQL_INTEGER:
		return "INTEGER"
	case api.SQL_SMALLINT:
		return "SMALLINT"
	case api.SQL_FLOAT:
		return "FLOAT"
	case api.SQL_REAL:
		return "REAL"
	case api.SQL_DOUBLE:
		return "DOUBLE"
	case api.SQL_DATETIME:
		return "DATETIME"
	case api.SQL_TIME:
		return "TIME"
	case api.SQL_VARCHAR:
		return "VARCHAR"
	case api.SQL_TYPE_DATE:
		return "TYPE_DATE"
	case api.SQL_TYPE_TIME:
		return "TYPE_TIME"
	case api.SQL_TYPE_TIMESTAMP:
		return "TYPE_TIMESTAMP"
	case api.SQL_TIMESTAMP:
		return "TIMESTAMP"
	case api.SQL_LONGVARCHAR:
		return "LONGVARCHAR"
	case api.SQL_BINARY:
		return "BINARY"
	case api.SQL_VARBINARY:
		return "VARBINARY"
	case api.SQL_LONGVARBINARY:
		return "LONGVARBINARY"
	case api.SQL_BIGINT:
		return "BIGINT"
	case api.SQL_TINYINT:
		return "TINYINT"
	case api.SQL_BIT:
		return "BIT"
	case api.SQL_WCHAR:
		return "WCHAR"
	case api.SQL_WVARCHAR:
		return "WVARCHAR"
	case api.SQL_WLONGVARCHAR:
		return "WLONGVARCHAR"
	case api.SQL_GUID:
		return "GUID"
	case api.SQL_SIGNED_OFFSET:
		return "SIGNED_OFFSET"
	case api.SQL_UNSIGNED_OFFSET:
		return "UNSIGNED_OFFSET"
	case api.SQL_UNKNOWN_TYPE:
		return ""
	default:
		return ""
	}
}

func (c *BaseColumn) Value(buf []byte) (driver.Value, error) {
	var p unsafe.Pointer
	if len(buf) > 0 {
		p = unsafe.Pointer(&buf[0])
	}
	switch c.CType {
	case api.SQL_C_BIT:
		return buf[0] != 0, nil
	case api.SQL_C_LONG:
		return *((*int32)(p)), nil
	case api.SQL_C_SBIGINT:
		return *((*int64)(p)), nil
	case api.SQL_C_DOUBLE:
		return *((*float64)(p)), nil
	case api.SQL_C_CHAR:
		return buf, nil
	case api.SQL_C_WCHAR:
		if p == nil {
			return buf, nil
		}
		s := unsafe.Slice((*uint16)(p), len(buf)/2)
		return utf16toutf8(s), nil
	case api.SQL_C_TYPE_TIMESTAMP:
		t := (*api.SQL_TIMESTAMP_STRUCT)(p)
		r := time.Date(int(t.Year), time.Month(t.Month), int(t.Day),
			int(t.Hour), int(t.Minute), int(t.Second), int(t.Fraction),
			time.Local)
		return r, nil
	case api.SQL_C_GUID:
		t := (*api.SQLGUID)(p)
		var p1, p2 string
		for _, d := range t.Data4[:2] {
			p1 += fmt.Sprintf("%02x", d)
		}
		for _, d := range t.Data4[2:] {
			p2 += fmt.Sprintf("%02x", d)
		}
		r := fmt.Sprintf("%08x-%04x-%04x-%s-%s",
			t.Data1, t.Data2, t.Data3, p1, p2)
		return r, nil
	case api.SQL_C_DATE:
		t := (*api.SQL_DATE_STRUCT)(p)
		r := time.Date(int(t.Year), time.Month(t.Month), int(t.Day),
			0, 0, 0, 0, time.Local)
		return r, nil
	case api.SQL_C_TIME:
		t := (*api.SQL_TIME_STRUCT)(p)
		r := time.Date(1, time.January, 1,
			int(t.Hour), int(t.Minute), int(t.Second), 0, time.Local)
		return r, nil
	case api.SQL_C_BINARY:
		if c.SQLType == api.SQL_SS_TIME2 {
			t := (*api.SQL_SS_TIME2_STRUCT)(p)
			r := time.Date(1, time.January, 1,
				int(t.Hour), int(t.Minute), int(t.Second), int(t.Fraction),
				time.Local)
			return r, nil
		}
		return buf, nil
	}
	return nil, fmt.Errorf("unsupported column ctype %d", c.CType)
}

// BindableColumn allows access to columns that can have their buffers
// bound. Once bound at start, they are written to by odbc driver every
// time it fetches new row. This saves on syscall and, perhaps, some
// buffer copying. BindableColumn can be left unbound, then it behaves
// like NonBindableColumn when user reads data from it.
type BindableColumn struct {
	*BaseColumn
	IsBound         bool
	IsVariableWidth bool
	Size            int
	Len             BufferLen
	Buffer          []byte
	// pinner keeps pinned Buffer and Len, whose addresses the driver
	// retains between SQLBindCol and the SQLFetch calls that fill them.
	// A BindableColumn must not be copied after its first bind.
	pinner runtime.Pinner
}

// TODO(brainman): BindableColumn.Buffer is used by external code after external code returns - that needs to be avoided in the future

func NewBindableColumn(b *BaseColumn, ctype api.SQLSMALLINT, bufSize int) *BindableColumn {
	b.CType = ctype
	c := &BindableColumn{BaseColumn: b, Size: bufSize}
	l := 8 // always use small starting buffer
	if c.Size > l {
		l = c.Size
	}
	c.Buffer = make([]byte, l)
	return c
}

func NewVariableWidthColumn(b *BaseColumn, ctype api.SQLSMALLINT, colWidth api.SQLULEN) (Column, error) {
	if colWidth == 0 || colWidth > 1024 {
		b.CType = ctype
		return &NonBindableColumn{b}, nil
	}
	l := int(colWidth)
	switch ctype {
	case api.SQL_C_WCHAR:
		l += 1 // room for null-termination character
		l *= 2 // wchars take 2 bytes each
	case api.SQL_C_CHAR:
		l += 1 // room for null-termination character
	case api.SQL_C_BINARY:
		// nothing to do
	default:
		return nil, fmt.Errorf("do not know how wide column of ctype %d is", ctype)
	}
	c := NewBindableColumn(b, ctype, l)
	c.IsVariableWidth = true
	return c, nil
}

func (c *BindableColumn) Bind(h api.SQLHSTMT, idx int) (bool, error) {
	ret := c.Len.Bind(h, idx, c.CType, c.Buffer)
	if IsError(ret) {
		return false, NewError("SQLBindCol", h)
	}
	c.IsBound = true
	// The driver fills Buffer and Len at every SQLFetch, after this cgo
	// call has returned: pin them until unbind or statement close.
	c.pinner.Unpin()
	c.pinner.Pin(unsafe.Pointer(&c.Buffer[0]))
	c.pinner.Pin(unsafe.Pointer(&c.Len))
	return true, nil
}

// Unbind releases column binding made by Bind.
func (c *BindableColumn) Unbind(h api.SQLHSTMT, idx int) error {
	ret := api.SQLBindCol(h, api.SQLUSMALLINT(idx+1), c.CType, nil, 0, nil)
	if IsError(ret) {
		return NewError("SQLBindCol", h)
	}
	c.IsBound = false
	c.pinner.Unpin()
	return nil
}

// errBoundTruncated is returned by BindableColumn.Value when odbc driver
// could not fit column data into bound buffer. Caller is expected to
// unbind the column (and all columns after it) and call Value again.
var errBoundTruncated = errors.New("odbc: bound column value truncated")

// nullTerminatorSize returns size of null-termination character that
// odbc driver appends to data of ctype.
func nullTerminatorSize(ctype api.SQLSMALLINT) int {
	switch ctype {
	case api.SQL_C_WCHAR:
		return 2
	case api.SQL_C_CHAR:
		return 1
	}
	return 0
}

func (c *BindableColumn) Value(h api.SQLHSTMT, idx int) (driver.Value, error) {
	if !c.IsBound {
		ret := c.Len.GetData(h, idx, c.CType, c.Buffer)
		if IsError(ret) {
			return nil, NewError("SQLGetData", h)
		}
	}
	if c.Len.IsNull() {
		// is NULL
		return nil, nil
	}
	if n := len(c.Buffer) - nullTerminatorSize(c.CType); c.IsVariableWidth && (c.Len < 0 || int(c.Len) > n) {
		// Driver truncated the data (for example, when it converts
		// character set). Len is either SQL_NO_TOTAL or the full
		// length of the data.
		if c.IsBound {
			return nil, errBoundTruncated
		}
		// Keep what we have received, and read the rest in chunks.
		total := append([]byte(nil), c.Buffer[:n]...)
		return c.BaseColumn.getDataChunked(h, idx, total)
	}
	if !c.IsVariableWidth && int(c.Len) != c.Size {
		return nil, fmt.Errorf("wrong column #%d length %d returned, %d expected", idx, c.Len, c.Size)
	}
	return c.BaseColumn.Value(c.Buffer[:c.Len])
}

// NonBindableColumn provide access to columns, that can't be bound.
// These are of character or binary type, and, usually, there is no
// limit for their width.
type NonBindableColumn struct {
	*BaseColumn
}

func (c *NonBindableColumn) Bind(h api.SQLHSTMT, idx int) (bool, error) {
	return false, nil
}

func (c *NonBindableColumn) Value(h api.SQLHSTMT, idx int) (driver.Value, error) {
	return c.BaseColumn.getDataChunked(h, idx, nil)
}

// getDataChunked reads column data with SQLGetData in chunks, until
// all data is received. Data received already, if any, is passed in
// total, and the rest is appended to it.
func (c *BaseColumn) getDataChunked(h api.SQLHSTMT, idx int, total []byte) (driver.Value, error) {
	var l BufferLen
	b := make([]byte, 1024)
loop:
	for {
		ret := l.GetData(h, idx, c.CType, b)
		switch ret {
		case api.SQL_SUCCESS:
			if l.IsNull() {
				// is NULL
				return nil, nil
			}
			if int(l) > len(b) {
				return nil, fmt.Errorf("too much data returned: %d bytes returned, but buffer size is %d", l, cap(b))
			}
			total = append(total, b[:l]...)
			break loop
		case api.SQL_SUCCESS_WITH_INFO:
			err := NewError("SQLGetData", h)
			diagErr, ok := err.(*Error)
			if !ok {
				// NewError failed to collect diagnostics; return the
				// original error instead of panicking on a type assertion.
				return nil, err
			}
			if len(diagErr.Diag) > 0 {
				truncated := false
				for _, diag := range diagErr.Diag {
					if diag.State == "01004" {
						truncated = true
						break
					}
				}
				if !truncated {
					return nil, diagErr
				}
			}
			i := len(b)
			switch c.CType {
			case api.SQL_C_WCHAR:
				i -= 2 // remove wchar (2 bytes) null-termination character
			case api.SQL_C_CHAR:
				i-- // remove null-termination character
			}
			total = append(total, b[:i]...)
			if l != api.SQL_NO_TOTAL {
				// odbc gives us a hint about remaining data,
				// lets get it in one go.
				n := int(l) // total bytes for our data
				n -= i      // subtract already received
				n += 2      // room for biggest (wchar) null-terminator
				if len(b) < n {
					b = make([]byte, n)
				}
			}
		default:
			return nil, NewError("SQLGetData", h)
		}
	}
	return c.Value(total)
}
