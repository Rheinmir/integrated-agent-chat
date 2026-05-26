package mcp

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Tool is the unit of capability exposed to the AI model.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     func(args map[string]any) (any, error)
}

// NewRegistry returns all tools available to the agent.
// Add your own tools here and they will automatically appear in every provider call.
func NewRegistry(db *sql.DB) []Tool {
	return []Tool{
		rememberTool(db),
		recallTool(db),
		forgetTool(db),
		searchTool(db),
	}
}

func rememberTool(db *sql.DB) Tool {
	return Tool{
		Name:        "remember",
		Description: "Store a fact about the user in persistent memory.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"key":   map[string]any{"type": "string", "description": "Short identifier for the fact"},
				"value": map[string]any{"type": "string", "description": "The value to remember"},
			},
			"required": []string{"key", "value"},
		},
		Handler: func(args map[string]any) (any, error) {
			key, _ := args["key"].(string)
			value, _ := args["value"].(string)
			if key == "" || value == "" {
				return nil, fmt.Errorf("remember: key and value are required")
			}
			_, err := db.Exec(
				`INSERT INTO agent_memory(key, value, updated_at) VALUES(?,?,?)
				 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
				key, value, time.Now().UTC().Format(time.RFC3339),
			)
			if err != nil {
				return nil, fmt.Errorf("remember: %w", err)
			}
			return map[string]any{"status": "remembered", "key": key}, nil
		},
	}
}

func recallTool(db *sql.DB) Tool {
	return Tool{
		Name:        "recall",
		Description: "Search persistent memory for facts matching a query.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Search term"},
			},
			"required": []string{"query"},
		},
		Handler: func(args map[string]any) (any, error) {
			query, _ := args["query"].(string)
			if query == "" {
				return nil, fmt.Errorf("recall: query is required")
			}

			words := strings.Fields(query)
			reversed := make([]string, len(words))
			for i, w := range words {
				reversed[len(words)-1-i] = w
			}
			reversedPhrase := strings.Join(reversed, " ")

			var conditions []string
			var params []any

			phrase := "%" + query + "%"
			conditions = append(conditions, "(key LIKE ? OR value LIKE ?)")
			params = append(params, phrase, phrase)

			if reversedPhrase != query {
				rPhrase := "%" + reversedPhrase + "%"
				conditions = append(conditions, "(key LIKE ? OR value LIKE ?)")
				params = append(params, rPhrase, rPhrase)
			}

			for _, w := range words {
				if len(w) > 1 {
					wp := "%" + w + "%"
					conditions = append(conditions, "(key LIKE ? OR value LIKE ?)")
					params = append(params, wp, wp)
				}
			}

			sqlStr := `SELECT key, value FROM agent_memory WHERE ` +
				strings.Join(conditions, " OR ") +
				` ORDER BY updated_at DESC LIMIT 20`

			rows, err := db.Query(sqlStr, params...)
			if err != nil {
				return nil, fmt.Errorf("recall: %w", err)
			}
			defer rows.Close()

			var results []map[string]string
			for rows.Next() {
				var k, v string
				if err := rows.Scan(&k, &v); err != nil {
					continue
				}
				results = append(results, map[string]string{"key": k, "value": v})
			}
			if len(results) == 0 {
				return map[string]any{"found": false, "results": []any{}}, nil
			}
			return map[string]any{"found": true, "results": results}, nil
		},
	}
}

func forgetTool(db *sql.DB) Tool {
	return Tool{
		Name:        "forget",
		Description: "Delete a specific fact from persistent memory by its key.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"key": map[string]any{"type": "string", "description": "The key to delete"},
			},
			"required": []string{"key"},
		},
		Handler: func(args map[string]any) (any, error) {
			key, _ := args["key"].(string)
			if key == "" {
				return nil, fmt.Errorf("forget: key is required")
			}
			res, err := db.Exec(`DELETE FROM agent_memory WHERE key = ?`, key)
			if err != nil {
				return nil, fmt.Errorf("forget: %w", err)
			}
			n, _ := res.RowsAffected()
			return map[string]any{"deleted": n > 0, "key": key}, nil
		},
	}
}

func searchTool(db *sql.DB) Tool {
	return Tool{
		Name:        "search_memory_demo",
		Description: "Demo search tool — searches agent_memory. Replace with a real domain tool.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Search term"},
			},
			"required": []string{"query"},
		},
		Handler: func(args map[string]any) (any, error) {
			query, _ := args["query"].(string)
			rows, err := db.Query(
				`SELECT key, value FROM agent_memory WHERE key LIKE ? ORDER BY updated_at DESC LIMIT 10`,
				"%"+query+"%",
			)
			if err != nil {
				return nil, fmt.Errorf("search: %w", err)
			}
			defer rows.Close()

			var results []map[string]string
			for rows.Next() {
				var k, v string
				if err := rows.Scan(&k, &v); err != nil {
					continue
				}
				results = append(results, map[string]string{"key": k, "value": v})
			}
			return map[string]any{"results": results, "count": len(results)}, nil
		},
	}
}
