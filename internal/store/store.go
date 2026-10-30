package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"wtable/internal/model"

	_ "modernc.org/sqlite"
)

type State struct {
	Denied  []model.DeniedEvent `json:"denied,omitempty"`
	Reports []model.Report      `json:"reports"`
	Version int                 `json:"version"`
	Panes   []model.Pane        `json:"panes"`
	Tasks   []model.Task        `json:"tasks"`
	Edges   []model.Edge        `json:"edges"`
	Layers  []model.Layer       `json:"layers"`
}

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA busy_timeout=5000", "CREATE TABLE IF NOT EXISTS state (id INTEGER PRIMARY KEY CHECK(id=1), body TEXT NOT NULL)"} {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Load() (State, error) {
	state := State{Version: 1}
	var raw string
	err := s.db.QueryRow("SELECT body FROM state WHERE id=1").Scan(&raw)
	if err == sql.ErrNoRows {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err = json.Unmarshal([]byte(raw), &state); err != nil {
		return state, err
	}
	if state.Version != 1 {
		return state, fmt.Errorf("不支持状态文件版本 %d", state.Version)
	}
	return state, nil
}

// One durable row is the MVP transaction boundary. A single daemon serializes
// mutations; task result and completion state therefore commit together.
func (s *Store) Save(state State) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO state(id,body) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body", string(raw))
	return err
}

func (s *Store) Close() error { return s.db.Close() }
