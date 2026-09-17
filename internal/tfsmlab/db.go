// internal/tfsmlab/db.go
//
// The template database as the Lab edits it: admin.py's list, platforms, get,
// create, update and delete.
//
// # rowid, not id
//
// netlapse addresses templates by the id column, and that column is `id INT`,
// not INTEGER PRIMARY KEY -- it is not an alias of rowid, it is never
// assigned, and its INSERT does not set it. A template created in netlapse's
// Lab therefore has a NULL id, and netlapse's own GET/PUT/DELETE
// `WHERE id = ?` cannot find it again. The shipped database already holds
// five such rows. The Lab here addresses every row by rowid, which every row
// has, and gives a new row an id (one past the largest) so netlapse can reach
// it too.
//
// Each call opens the database, does its work and closes it. The engine that
// captures use holds templates in memory, not a connection, so a write here is
// seen by a capture only after that engine reloads (capi does it).
package tfsmlab

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// ErrNotFound is a rowid with no row.
var ErrNotFound = errors.New("template not found")

// Platform is one platform prefix and how many templates carry it.
type Platform struct {
	Platform string `json:"platform"`
	Count    int    `json:"count"`
}

// Item is one row in a list.
type Item struct {
	RowID  int64  `json:"rowid"`
	ID     *int64 `json:"id"`
	Name   string `json:"cli_command"`
	Source string `json:"source"`
}

// Row is a whole template.
type Row struct {
	Item
	Content string `json:"textfsm_content"`
	Sample  string `json:"cli_content"`
	Hash    string `json:"textfsm_hash"`
	Created string `json:"created"`
}

func open(path string, write bool) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("tfsmlab: no database path")
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)"
	if !write {
		dsn += "&mode=ro"
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("tfsmlab: open %s: %w", path, err)
	}
	return db, nil
}

// Platforms is admin.py's list_template_platforms: the first two '_' parts of
// each distinct name, counted, sorted. A name with no '_' has no platform.
func Platforms(path string) ([]Platform, error) {
	db, err := open(path, false)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT DISTINCT cli_command FROM templates WHERE cli_command IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("tfsmlab: read platforms: %w", err)
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if parts := strings.Split(name, "_"); len(parts) >= 2 {
			counts[parts[0]+"_"+parts[1]]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []Platform{}
	for p, c := range counts {
		out = append(out, Platform{p, c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Platform < out[j].Platform })
	return out, nil
}

// List is admin.py's list_templates without its page limit: names starting
// with platform and containing query, both as SQLite LIKE (ASCII
// case-insensitive, and '%' and '_' in either are wildcards, as there), by name.
func List(path, platform, query string) ([]Item, error) {
	db, err := open(path, false)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	q := `SELECT rowid, id, COALESCE(cli_command, ''), COALESCE(source, '') FROM templates WHERE 1=1`
	var args []any
	if platform != "" {
		q += ` AND cli_command LIKE ?`
		args = append(args, platform+"%")
	}
	if query != "" {
		q += ` AND cli_command LIKE ?`
		args = append(args, "%"+query+"%")
	}
	q += ` ORDER BY cli_command, rowid`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("tfsmlab: list templates: %w", err)
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var it Item
		var id sql.NullInt64
		if err := rows.Scan(&it.RowID, &id, &it.Name, &it.Source); err != nil {
			return nil, err
		}
		if id.Valid {
			v := id.Int64
			it.ID = &v
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Get returns one template by rowid.
func Get(path string, rowid int64) (Row, error) {
	db, err := open(path, false)
	if err != nil {
		return Row{}, err
	}
	defer db.Close()
	var r Row
	var id sql.NullInt64
	err = db.QueryRow(`SELECT rowid, id, COALESCE(cli_command, ''), COALESCE(source, ''),
		COALESCE(textfsm_content, ''), COALESCE(cli_content, ''), COALESCE(textfsm_hash, ''),
		COALESCE(created, '') FROM templates WHERE rowid = ?`, rowid).
		Scan(&r.RowID, &id, &r.Name, &r.Source, &r.Content, &r.Sample, &r.Hash, &r.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return Row{}, ErrNotFound
	}
	if err != nil {
		return Row{}, fmt.Errorf("tfsmlab: read template %d: %w", rowid, err)
	}
	if id.Valid {
		v := id.Int64
		r.ID = &v
	}
	return r, nil
}

// Create adds a template and returns its rowid and the id it was given.
//
// Two refusals netlapse does not make, because its table has no constraint to
// make them: an empty name, and a name another row already has. A second row
// under one name is almost always a Save after Clear that meant to edit, and
// it leaves two templates the Lab and the capture log cannot tell apart.
func Create(path, name, content string) (rowid, id int64, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, 0, errors.New("a template needs a name")
	}
	db, err := open(path, true)
	if err != nil {
		return 0, 0, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return 0, 0, fmt.Errorf("tfsmlab: %w", err)
	}
	defer tx.Rollback()
	if err := refuseDuplicate(tx, name, 0); err != nil {
		return 0, 0, err
	}
	if err := tx.QueryRow(`SELECT COALESCE(MAX(id), 0) + 1 FROM templates`).Scan(&id); err != nil {
		return 0, 0, fmt.Errorf("tfsmlab: next id: %w", err)
	}
	res, err := tx.Exec(`INSERT INTO templates (id, cli_command, textfsm_content) VALUES (?, ?, ?)`, id, name, content)
	if err != nil {
		return 0, 0, fmt.Errorf("tfsmlab: add %s: %w", name, err)
	}
	if rowid, err = res.LastInsertId(); err != nil {
		return 0, 0, fmt.Errorf("tfsmlab: add %s: %w", name, err)
	}
	return rowid, id, tx.Commit()
}

// Update changes a template's name and content, as admin.py's update does:
// those two columns and nothing else -- not source, not textfsm_hash. A row
// with no id is given one, so netlapse can address it afterwards.
func Update(path string, rowid int64, name, content string) (id int64, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, errors.New("a template needs a name")
	}
	db, err := open(path, true)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("tfsmlab: %w", err)
	}
	defer tx.Rollback()
	var cur sql.NullInt64
	err = tx.QueryRow(`SELECT id FROM templates WHERE rowid = ?`, rowid).Scan(&cur)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("tfsmlab: read template %d: %w", rowid, err)
	}
	if err := refuseDuplicate(tx, name, rowid); err != nil {
		return 0, err
	}
	id = cur.Int64
	if !cur.Valid {
		if err := tx.QueryRow(`SELECT COALESCE(MAX(id), 0) + 1 FROM templates`).Scan(&id); err != nil {
			return 0, fmt.Errorf("tfsmlab: next id: %w", err)
		}
	}
	if _, err := tx.Exec(`UPDATE templates SET cli_command = ?, textfsm_content = ?, id = ? WHERE rowid = ?`,
		name, content, id, rowid); err != nil {
		return 0, fmt.Errorf("tfsmlab: update %s: %w", name, err)
	}
	return id, tx.Commit()
}

// Delete removes a template by rowid.
func Delete(path string, rowid int64) error {
	db, err := open(path, true)
	if err != nil {
		return err
	}
	defer db.Close()
	res, err := db.Exec(`DELETE FROM templates WHERE rowid = ?`, rowid)
	if err != nil {
		return fmt.Errorf("tfsmlab: delete template %d: %w", rowid, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func refuseDuplicate(tx *sql.Tx, name string, self int64) error {
	var other int64
	err := tx.QueryRow(`SELECT rowid FROM templates WHERE cli_command = ? AND rowid != ? LIMIT 1`, name, self).Scan(&other)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("tfsmlab: check name: %w", err)
	}
	return fmt.Errorf("a template named %s already exists (row %d); open it to edit it", name, other)
}
