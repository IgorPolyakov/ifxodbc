// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
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
