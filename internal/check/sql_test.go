package check

import (
	"strings"
	"testing"
)

func TestSQLTypes(t *testing.T) {
	expectCodes(t, `
import "sql"

struct User { id: Int, name: Str }

fn all(conn: sql.Conn) -> !List[User] uses sql {
    return try sql.query_as(conn, text: sql"SELECT id, name FROM users", schema: User)
}

fn raw(conn: sql.Conn) -> !List[Map[Str, Any]] uses sql {
    return try sql.query(conn, text: sql"SELECT id FROM users WHERE id = $1", args: [1])
}

fn bump(conn: sql.Conn) -> !Int uses sql {
    return try sql.exec(conn, text: sql"UPDATE users SET name = ? WHERE id = ?", args: ["ada", 1])
}
`)
}

// A connection is a handle, and the `sql` capability must be declared.
func TestSQLRequiresEffect(t *testing.T) {
	expectCodes(t, `
import "sql"

fn bump(conn: sql.Conn) -> !Int {
    return try sql.exec(conn, text: sql"UPDATE users SET name = $1", args: ["ada"])
}
`, "E0501")

	expectCodes(t, `
import "sql"

fn bad(conn: sql.Conn) -> !Int uses sql, bogus {
    return try sql.exec(conn, text: sql"SELECT 1")
}
`, "E0502", "W0503")
}

// A query is Sql text: a plain Str is rejected with the safe fix hint, the
// placeholders must be named, and args must be a List.
func TestSQLArgumentRules(t *testing.T) {
	ds := expectCodes(t, `
import "sql"

fn q(conn: sql.Conn) -> !List[Map[Str, Any]] uses sql {
    return try sql.query(conn, text: "SELECT 1")
}
`, "E0301")
	if !strings.Contains(ds[0].Hint, `sql"..."`) {
		t.Fatalf("hint: %q", ds[0].Hint)
	}

	expectCodes(t, `
import "sql"

fn q(conn: sql.Conn) -> !List[Map[Str, Any]] uses sql {
    return try sql.query(conn, sql"SELECT 1")
}
`, "E0306")

	expectCodes(t, `
import "sql"

fn q(conn: sql.Conn) -> !List[Map[Str, Any]] uses sql {
    return try sql.query(conn, text: sql"SELECT ?", args: "one")
}
`, "E0301")
}
