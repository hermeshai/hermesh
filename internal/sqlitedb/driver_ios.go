//go:build libsqlite3 && sqlite_omit_load_extension

package sqlitedb

// The iOS core uses the OS SQLite library. The bundled Darwin SQLite probes
// filesystem types and locking modes with statfs/fstatfs even for app data.
import _ "github.com/mattn/go-sqlite3"

const (
	driverName        = "sqlite3"
	busyTimeout       = "_busy_timeout=5000"
	writerOptions     = busyTimeout + "&_journal_mode=WAL&_foreign_keys=1&_synchronous=NORMAL"
	readerOptions     = busyTimeout + "&_query_only=1&_foreign_keys=1&_synchronous=FULL"
	checkpointOptions = busyTimeout + "&_synchronous=FULL"
)
