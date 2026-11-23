package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeMariaDB is a client that keeps each database as a .sql file under
// $FAKEDB and answers the few catalog queries the engine sends. It never opens
// a connection, so a test using it cannot touch a real server. BT stands for a
// backtick, which a Go raw string cannot hold.
const fakeMariaDB = `#!/bin/sh
db=""; sql=""; prev=""; last=""
for a do
  case "$prev" in -D) db="$a";; -e) sql="$a";; esac
  prev="$a"; last="$a"
done
if [ -z "$sql" ]; then
  cat > "$FAKEDB/$last.sql"
  exit 0
fi
printf '%s\n' "$sql" >> "$FAKEDB/queries.log"
schema=$(printf '%s' "$sql" | sed -n "s/.*table_schema='\([^']*\)'.*/\1/p")
case "$sql" in
 *"COUNT(*) FROM information_schema.tables"*)
   n=$(grep -c 'CREATE TABLE' "$FAKEDB/$schema.sql" 2>/dev/null); [ -n "$n" ] || n=0
   printf 'COUNT(*)\n%s\n' "$n";;
 *"table_name FROM information_schema.tables"*)
   echo table_name; sed -n 's/^CREATE TABLE BT\([^BT]*\)BT.*/\1/p' "$FAKEDB/$schema.sql" 2>/dev/null;;
 *"information_schema.SCHEMATA"*) printf 'COUNT(*)\n1\n';;
 *"SELECT host FROM mysql.user"*) printf 'host\nlocalhost\n127.0.0.1\n';;
 *"DROP DATABASE IF EXISTS"*)
   d=$(printf '%s' "$sql" | sed -n 's/^DROP DATABASE IF EXISTS BT\{0,1\}\([A-Za-z0-9_]*\).*/\1/p')
   rm -f "$FAKEDB/$d.sql";;
esac
exit 0
`

const fakeMariaDBDump = `#!/bin/sh
for a do last="$a"; done
if [ -f "$FAKEDB/$last.sql" ]; then cat "$FAKEDB/$last.sql"; else echo "-- empty $last"; fi
`

// withFakeDB points the engine's MySQL inventory at the fakes and makes sure
// something answers on the database port, so EnsureDB takes its running path.
// It returns the directory holding the fake databases.
func withFakeDB(t *testing.T, e *Engine) string {
	t.Helper()
	bin := t.TempDir()
	state := t.TempDir()
	scripts := map[string]string{
		"mariadb":      strings.ReplaceAll(fakeMariaDB, "BT", "`"),
		"mariadb-dump": fakeMariaDBDump,
		"mysqld":       "#!/bin/sh\nexit 1\n",
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("FAKEDB", state)
	inv := e.Store.Inventory()
	inv.MySQL.Bin = filepath.Join(bin, "mysqld")
	inv.MySQL.Kind = "mariadb"
	// Fresh, so a CLI command's own store does not rescan and find the real one.
	inv.Refresh = time.Now()
	if err := e.Store.Save(); err != nil {
		t.Fatal(err)
	}
	// The fake is always up. A real port probe made this pass on a machine
	// running MariaDB and fail on CI, where nothing reliably held the port.
	old := dbPortOpen
	dbPortOpen = func() bool { return true }
	t.Cleanup(func() { dbPortOpen = old })
	return state
}
