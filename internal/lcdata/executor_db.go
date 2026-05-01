package lcdata

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/go-sql-driver/mysql"
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
	switch driver {
	case "postgres", "postgresql":
		driver = "postgres"
	case "sqlite", "sqlite3":
		driver = "sqlite"
	case "mysql", "mariadb":
		driver = "mysql"
	}

	db, err := sql.Open(driver, connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	switch strings.ToLower(node.Operation) {
	case "exec":
		return dbExec(ctx, db, node, inputs)
	case "lookup":
		return dbLookup(ctx, db, node, inputs, driver, events)
	default:
		return dbQuery(ctx, db, node, inputs, events)
	}
}

// dbQuery runs a SELECT and returns all rows.
// Params are resolved from inputs: "{{.input.foo}}" → inputs["foo"].
func dbQuery(
	ctx context.Context,
	db *sql.DB,
	node *Node,
	inputs map[string]any,
	events chan<- Event,
) (map[string]any, error) {
	params := resolveParams(node.Params, inputs)

	rows, err := db.QueryContext(ctx, node.Query, params...)
	if err != nil {
		return nil, fmt.Errorf("query error: %w", err)
	}
	defer rows.Close()

	return scanRows(rows)
}

// dbExec runs an INSERT/UPDATE/DELETE and returns rows_affected.
func dbExec(
	ctx context.Context,
	db *sql.DB,
	node *Node,
	inputs map[string]any,
) (map[string]any, error) {
	params := resolveParams(node.Params, inputs)

	result, err := db.ExecContext(ctx, node.Query, params...)
	if err != nil {
		return nil, fmt.Errorf("exec error: %w", err)
	}
	rowsAffected, _ := result.RowsAffected()
	return map[string]any{
		"rows_affected": rowsAffected,
		"ok":            true,
	}, nil
}

// dbLookup performs a SELECT … WHERE id IN (…) against an array of IDs from inputs["ids"].
// The query should use a placeholder: SELECT * FROM t WHERE id IN (?)
// dbLookup replaces the single ? with the correct number of driver-specific placeholders.
func dbLookup(
	ctx context.Context,
	db *sql.DB,
	node *Node,
	inputs map[string]any,
	driver string,
	events chan<- Event,
) (map[string]any, error) {
	// ids must be []any (array of id strings from search results)
	rawIDs, ok := inputs["ids"]
	if !ok || rawIDs == nil {
		return map[string]any{"rows": []any{}, "count": 0}, nil
	}

	var ids []any
	switch v := rawIDs.(type) {
	case []any:
		// Accept either []string or [{id: "...", score: ...}] (springg search results)
		for _, item := range v {
			switch id := item.(type) {
			case string:
				ids = append(ids, id)
			case map[string]any:
				if idStr, ok := id["id"].(string); ok {
					ids = append(ids, idStr)
				}
			}
		}
	case []string:
		ids = make([]any, len(v))
		for i, s := range v {
			ids[i] = s
		}
	default:
		return nil, fmt.Errorf("input.ids must be an array, got %T", rawIDs)
	}

	if len(ids) == 0 {
		return map[string]any{"rows": []any{}, "count": 0}, nil
	}

	// Build IN clause placeholders
	placeholders := make([]string, len(ids))
	for i := range ids {
		switch driver {
		case "postgres":
			placeholders[i] = fmt.Sprintf("$%d", i+1)
		default:
			placeholders[i] = "?"
		}
	}
	query := strings.Replace(node.Query, "?", strings.Join(placeholders, ","), 1)

	rows, err := db.QueryContext(ctx, query, ids...)
	if err != nil {
		return nil, fmt.Errorf("lookup query error: %w", err)
	}
	defer rows.Close()

	return scanRows(rows)
}

// resolveParams converts node.Params — which may contain "{{.input.key}}" references — to []any.
func resolveParams(paramTemplates []string, inputs map[string]any) []any {
	params := make([]any, len(paramTemplates))
	for i, p := range paramTemplates {
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
	return params
}

// scanRows reads all rows from a sql.Rows into []any of map[string]any.
func scanRows(rows *sql.Rows) (map[string]any, error) {
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
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			row[col] = v
		}
		results = append(results, row)
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
