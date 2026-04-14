package lcdata

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

func executeDatabase(
	ctx context.Context,
	node *Node,
	inputs map[string]any,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {

	connStr, ok := env.DBConnections[node.Connection]
	if !ok || connStr == "" {
		return nil, fmt.Errorf("database connection %q not found in environment config", node.Connection)
	}

	driver := strings.ToLower(node.Driver)
	if driver == "" {
		driver = "postgres"
	}
	// Map friendly names to sql driver names
	switch driver {
	case "postgres", "postgresql":
		driver = "postgres"
	case "sqlite", "sqlite3":
		driver = "sqlite"
	}

	db, err := sql.Open(driver, connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	// Build params — render each against inputs
	params := make([]any, len(node.Params))
	for i, p := range node.Params {
		// Resolve simple input references like "{{.input.user_id}}"
		if strings.HasPrefix(p, "{{") {
			key := strings.TrimPrefix(p, "{{.input.")
			key = strings.TrimSuffix(key, "}}")
			key = strings.TrimSpace(key)
			if v, ok := inputs[key]; ok {
				params[i] = v
				continue
			}
		}
		params[i] = p
	}

	rows, err := db.QueryContext(ctx, node.Query, params...)
	if err != nil {
		return nil, fmt.Errorf("query error: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("failed to get columns: %w", err)
	}

	var results []any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("row scan error: %w", err)
		}

		row := make(map[string]any, len(cols))
		for i, col := range cols {
			v := vals[i]
			// Convert []byte to string for readability
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			row[col] = v
		}
		results = append(results, row)

		events <- Event{
			Event:     EventChunk,
			StepID:    node.Name,
			Data:      fmt.Sprintf("%v", row),
			Timestamp: time.Now(),
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	if results == nil {
		results = []any{}
	}

	return map[string]any{
		"rows":  results,
		"count": len(results),
	}, nil
}
