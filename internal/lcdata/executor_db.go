package lcdata

import (
	"context"
	"fmt"
)

// executeDatabase executes a SQL query against a named connection.
// Database drivers are registered separately — add imports in your build
// (e.g. _ "github.com/lib/pq" for Postgres, _ "github.com/go-sql-driver/mysql" for MySQL).
func executeDatabase(
	ctx context.Context,
	node *Node,
	inputs map[string]any,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {

	connStr, ok := env.DBConnections[node.Connection]
	if !ok {
		return nil, fmt.Errorf("database connection %q not found in environment config", node.Connection)
	}

	if connStr == "" {
		return nil, fmt.Errorf("connection string for %q is empty", node.Connection)
	}

	// TODO: open connection with node.Driver and connStr
	// TODO: render node.Query and node.Params against inputs
	// TODO: execute query and scan rows into []map[string]any
	// TODO: stream rows as EventChunk events
	//
	// Example (requires "database/sql" import and appropriate driver):
	//   db, err := sql.Open(node.Driver, connStr)
	//   rows, err := db.QueryContext(ctx, renderedQuery, params...)
	//   for rows.Next() { ... }

	_ = ctx
	_ = connStr

	return nil, fmt.Errorf("database executor: driver %q not yet wired — add the driver import and implement this function", node.Driver)
}
