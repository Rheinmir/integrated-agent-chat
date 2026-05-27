package mcp

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func randomHexID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

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
		createPlaylistTool(db),
		addToPlaylistTool(db),
		playPlaylistTool(db),
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

func createPlaylistTool(db *sql.DB) Tool {
	return Tool{
		Name:        "create_playlist",
		Description: "Create a new playlist. Returns playlist id — this is NOT a track id, do NOT pass it to play_track.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
			"required": []string{"name"},
		},
		Handler: func(args map[string]any) (any, error) {
			name, _ := args["name"].(string)
			name = strings.TrimSpace(name)
			if name == "" {
				return nil, fmt.Errorf("name required")
			}
			id := randomHexID()
			_, err := db.Exec(`INSERT INTO playlists (id, name) VALUES (?, ?)`, id, name)
			if err != nil {
				return nil, err
			}
			return map[string]any{"id": id, "name": name}, nil
		},
	}
}

func addToPlaylistTool(db *sql.DB) Tool {
	return Tool{
		Name:        "add_to_playlist",
		Description: "Add track to playlist. track_id must come from search_music or list_tracks, NOT from create_playlist.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"playlist_id": map[string]any{"type": "string"},
				"track_id":    map[string]any{"type": "string"},
			},
			"required": []string{"playlist_id", "track_id"},
		},
		Handler: func(args map[string]any) (any, error) {
			pid, _ := args["playlist_id"].(string)
			tid, _ := args["track_id"].(string)
			if pid == "" || tid == "" {
				return nil, fmt.Errorf("playlist_id and track_id required")
			}
			var pos int
			_ = db.QueryRow(`SELECT COALESCE(MAX(position),0)+1 FROM playlist_tracks WHERE playlist_id=?`, pid).Scan(&pos)
			_, err := db.Exec(`INSERT OR IGNORE INTO playlist_tracks (playlist_id, track_id, position) VALUES (?,?,?)`, pid, tid, pos)
			if err != nil {
				return nil, err
			}
			return map[string]any{"ok": true}, nil
		},
	}
}

func playPlaylistTool(db *sql.DB) Tool {
	return Tool{
		Name:        "play_playlist",
		Description: "Play first track of a playlist by playlist_id. Use after create_playlist + add_to_playlist.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"playlist_id": map[string]any{"type": "string"},
			},
			"required": []string{"playlist_id"},
		},
		Handler: func(args map[string]any) (any, error) {
			pid, _ := args["playlist_id"].(string)
			if pid == "" {
				return nil, fmt.Errorf("playlist_id required")
			}
			var trackID, albumID, title, artist string
			var durS int
			err := db.QueryRow(
				`SELECT t.id, t.album_id, t.title, ar.name, COALESCE(t.duration_s,0)
				 FROM playlist_tracks pt
				 JOIN tracks t ON t.id = pt.track_id
				 JOIN albums al ON al.id = t.album_id
				 JOIN artists ar ON ar.id = al.artist_id
				 WHERE pt.playlist_id = ?
				 ORDER BY pt.position ASC LIMIT 1`, pid).
				Scan(&trackID, &albumID, &title, &artist, &durS)
			if err == sql.ErrNoRows {
				return nil, fmt.Errorf("playlist is empty — add tracks first with add_to_playlist")
			}
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"_frontend_action": "play_track",
				"id":               trackID,
				"title":            title,
				"artist":           artist,
				"album_id":         albumID,
				"duration_s":       durS,
			}, nil
		},
	}
}
