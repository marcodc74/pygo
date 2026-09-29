package interp

import (
	"os"
	"strings"
	"testing"
)

// TestSQLPostgres is the real-server integration test: it needs a PostgreSQL
// reachable through PYGO_TEST_PG_DSN (set by the CI `postgres` job) and is
// skipped otherwise. The program runs on all three engines.
func TestSQLPostgres(t *testing.T) {
	dsn := os.Getenv("PYGO_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set PYGO_TEST_PG_DSN to run the PostgreSQL integration test")
	}
	src := strings.ReplaceAll(`
import "sql"

struct Row { id: Int, name: Str, price: Float }

fn main() -> ! uses sql {
    let c = try sql.open("__DSN__")
    let _ = try sql.exec(c, text: sql"DROP TABLE IF EXISTS pygo_sql_test")
    let _ = try sql.exec(c, text: sql"CREATE TABLE pygo_sql_test (id serial PRIMARY KEY, name text NOT NULL, price numeric(10,2) NOT NULL)")
    let _ = try sql.exec(c, text: sql"INSERT INTO pygo_sql_test (name, price) VALUES ($1, $2)", args: ["keyboard", 49.9])
    let _ = try sql.exec(c, text: sql"INSERT INTO pygo_sql_test (name, price) VALUES (?, ?)", args: ["mouse", 19.5])
    let rows = try sql.query_as(c, text: sql"SELECT id, name, price FROM pygo_sql_test ORDER BY id", schema: Row)
    for r in rows { print(r.name, r.price) }
    let n = try sql.exec(c, text: sql"UPDATE pygo_sql_test SET price = $1 WHERE name = $2", args: [55.0, "keyboard"])
    print("updated", n)
    let counts = try sql.query(c, text: sql"SELECT count(*) AS n FROM pygo_sql_test")
    print("count", counts[0]["n"])
    let _ = try sql.exec(c, text: sql"DROP TABLE pygo_sql_test")
    sql.close(c)
}
`, "__DSN__", dsn)
	out, res := runSrc(t, src, "sql")
	if res.Status != "ok" {
		t.Fatalf("status %s: %s\n%s", res.Status, res.Describe(), out)
	}
	want := "keyboard 49.9\nmouse 19.5\nupdated 1\ncount 2\n"
	if out != want {
		t.Fatalf("output\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}
