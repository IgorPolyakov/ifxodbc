// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package odbc

import (
	"fmt"
	"sync"

	"github.com/IgorPolyakov/ifxodbc/api"
)

type Stats struct {
	EnvCount  int
	ConnCount int
	StmtCount int
	mu        sync.Mutex
}

func (s *Stats) updateHandleCount(handleType api.SQLSMALLINT, change int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch handleType {
	case api.SQL_HANDLE_ENV:
		s.EnvCount += change
	case api.SQL_HANDLE_DBC:
		s.ConnCount += change
	case api.SQL_HANDLE_STMT:
		s.StmtCount += change
	default:
		return fmt.Errorf("unexpected handle type %d", handleType)
	}
	return nil
}

// StmtCountSnapshot returns the current statement handle count. Use it
// instead of copying the Stats struct: a copy also copies the mutex and
// goes stale as soon as counts change.
func (s *Stats) StmtCountSnapshot() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.StmtCount
}
