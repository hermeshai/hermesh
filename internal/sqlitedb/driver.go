//go:build !ios

package sqlitedb

import _ "modernc.org/sqlite"

const (
	driverName        = "sqlite"
	busyTimeout       = "_pragma=busy_timeout(5000)"
	writerOptions     = busyTimeout + "&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	readerOptions     = busyTimeout + "&_pragma=query_only(1)&_pragma=foreign_keys(1)"
	checkpointOptions = busyTimeout
)
