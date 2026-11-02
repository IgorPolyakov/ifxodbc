// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"strings"
	"testing"

	"github.com/IgorPolyakov/ifxodbc/api"
)

func TestNullTerminatorSize(t *testing.T) {
	tests := []struct {
		ctype api.SQLSMALLINT
		want  int
	}{
		{api.SQL_C_CHAR, 1},
		{api.SQL_C_WCHAR, 2},
		{api.SQL_C_BINARY, 0},
		{api.SQL_C_LONG, 0},
	}
	for _, test := range tests {
		if got := nullTerminatorSize(test.ctype); got != test.want {
			t.Errorf("nullTerminatorSize(%v): want=%v, got=%v", test.ctype, test.want, got)
		}
	}
}

// fakeDescribeColumn emulates SQLDescribeCol name handling: the name is
// truncated to fit the buffer with a NUL terminator, the full length
// without NUL is reported and SQL_SUCCESS_WITH_INFO signals truncation.
func fakeDescribeColumn(name string) describeColFn {
	return func(namebuf []uint16) (int, api.SQLRETURN) {
		r := []rune(name)
		fit := len(r)
		var ret api.SQLRETURN = api.SQL_SUCCESS
		if fit >= len(namebuf) {
			fit = len(namebuf) - 1
			ret = api.SQL_SUCCESS_WITH_INFO
		}
		for i := 0; i < fit; i++ {
			namebuf[i] = uint16(r[i])
		}
		namebuf[fit] = 0
		return len(r), ret
	}
}

func TestReadColumnName(t *testing.T) {
	// 150 is the size of the first guess buffer inside readColumnName;
	// with the old code names of exactly 150 runes and longer lost
	// their last character because the retry buffer had no room for NUL.
	for _, name := range []string{
		"",
		"a",
		strings.Repeat("x", 148),
		strings.Repeat("x", 149),
		strings.Repeat("x", 150),
		strings.Repeat("x", 151),
		strings.Repeat("x", 300),
	} {
		got, err := readColumnName(fakeDescribeColumn(name))
		if err != nil {
			t.Fatalf("name of %d runes: %v", len(name), err)
		}
		if got != name {
			t.Errorf("name of %d runes: got %q (len %d), want %q", len(name), got, len(got), name)
		}
	}
}

func TestReadColumnNameErrors(t *testing.T) {
	if _, err := readColumnName(func([]uint16) (int, api.SQLRETURN) {
		return 0, api.SQLRETURN(-1) // SQL_ERROR
	}); err == nil {
		t.Error("expected an error when SQLDescribeCol fails")
	}
	if _, err := readColumnName(func(namebuf []uint16) (int, api.SQLRETURN) {
		// a driver that always reports a name longer than the buffer
		return len(namebuf) + 1, api.SQL_SUCCESS_WITH_INFO
	}); err == nil {
		t.Error("expected an error when the name never fits the buffer")
	}
}
