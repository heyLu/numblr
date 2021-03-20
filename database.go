package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func InitDatabase(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?cache=shared&_busy_timeout=50")
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS feed_infos ( name TEXT PRIMARY KEY, url TEXT, cached_at DATE )`)
	if err != nil {
		return nil, fmt.Errorf("setup feed_infos table: %w", err)
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS posts ( source TEXT, name TEXT, id TEXT, author TEXT, avatar_url TEXT, url TEXT, title TEXT, description_html TEXT, tags TEXT, date_string TEXT, date DATE, PRIMARY KEY (source, name, id))`)
	if err != nil {
		return nil, fmt.Errorf("setup posts table: %w", err)
	}

	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS posts_by_author_and_date ON posts (author, date)`)
	if err != nil {
		return nil, fmt.Errorf("setup posts index: %w", err)
	}

	return db, err
}

func ListFeedsOlderThan(ctx context.Context, db *sql.DB, olderThan time.Time) ([]string, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Commit()

	rows, err := tx.Query(`SELECT name FROM feed_infos WHERE ? > cached_at`, olderThan)
	if err != nil {
		return nil, fmt.Errorf("select: %w", err)
	}
	defer rows.Close()

	feeds := make([]string, 0, 10)
	for rows.Next() {
		var feed string
		err := rows.Scan(&feed)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}

		feeds = append(feeds, feed)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("after scan: %w", rows.Err())
	}

	return feeds, nil
}

func NewDatabaseCached(ctx context.Context, db *sql.DB, name string, uncachedFn FeedFn, search Search) (Feed, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}

	row := tx.QueryRow("SELECT cached_at, url FROM feed_infos WHERE name = ?", name)
	var cachedAt time.Time
	var url string
	err = row.Scan(&cachedAt, &url)
	if err != nil && err != sql.ErrNoRows {
		tx.Rollback()
		return nil, fmt.Errorf("looking up feed: %w", err)
	}

	isCached := err != sql.ErrNoRows
	if isCached && time.Since(cachedAt) < CacheTime {
		var rows *sql.Rows
		if search.BeforeID != "" {
			rows, err = tx.Query("SELECT source, id, author, avatar_url, url, title, description_html, tags, date_string, date FROM posts WHERE author = ? AND id < ? ORDER BY date DESC LIMIT 20", name, search.BeforeID)
		} else {
			rows, err = tx.Query("SELECT source, id, author, avatar_url, url, title, description_html, tags, date_string, date FROM posts WHERE author = ? ORDER BY date DESC LIMIT 20", name)
		}
		if err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("querying posts: %w", err)
		}

		return &databaseCached{name: name, url: url, rows: rows, tx: tx}, nil
	}

	// close readonly tx, open new one later when saving
	tx.Rollback()

	feed, err := uncachedFn(ctx, name, search)
	if err != nil {
		return nil, fmt.Errorf("open uncached: %w", err)
	}
	return &databaseCaching{
		db:       db,
		uncached: feed,
		cachedAt: time.Now(),
		posts:    make([]*Post, 0, 10),
	}, nil
}

type databaseCaching struct {
	db       *sql.DB
	uncached Feed
	cachedAt time.Time
	posts    []*Post
}

func (ct *databaseCaching) Name() string {
	return ct.uncached.Name()
}

func (ct *databaseCaching) URL() string {
	return ct.uncached.URL()
}

func (ct *databaseCaching) Next() (*Post, error) {
	post, err := ct.uncached.Next()
	if err != nil {
		return nil, err
	}
	ct.posts = append(ct.posts, post)
	return post, nil
}

func (ct *databaseCaching) Close() error {
	err := ct.Save()
	if err != nil {
		ct.uncached.Close()
		return fmt.Errorf("saving: %w", err)
	}
	return ct.uncached.Close()
}

func (ct *databaseCaching) Save() error {
	tx, err := ct.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: false})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	stmt := `INSERT OR REPLACE INTO posts VALUES `
	vals := make([]interface{}, 0, len(ct.posts)*10)
	for _, post := range ct.posts {
		// 	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS posts ( name
		// 	TEXT, id TEXT, author TEXT, avatar_url TEXT, url TEXT, title
		// 	TEXT, description_html TEXT, tags TEXT, date_string TEXT, date TIME, PRIMARY KEY (name, id))`)

		if post.ID == "" {
			tx.Rollback()
			return fmt.Errorf("empty post id: %#v", post)
		}
		if post.Source == "" {
			tx.Rollback()
			return fmt.Errorf("empty post source: %#v", post)
		}

		tagsJSON, err := json.Marshal(post.Tags)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("encode tags: %w", err)
		}

		stmt += "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?), "
		vals = append(vals, post.Source, ct.uncached.Name(), post.ID, post.Author, post.AvatarURL, post.URL, post.Title, post.DescriptionHTML, tagsJSON, post.DateString, post.Date)
	}

	// trim last comma and space
	stmt = stmt[:len(stmt)-2]

	res, err := tx.Exec(stmt, vals...)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("update posts: %w", err)
	}

	res, err = tx.Exec(`INSERT OR REPLACE INTO feed_infos VALUES (?, ?, ?)`, ct.uncached.Name(), ct.uncached.URL(), ct.cachedAt)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("update feed_infos: %w", err)
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	count, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("row count: %w", err)
	}

	if count != 1 {
		return fmt.Errorf("expected 1 updated row, but was %d", count)
	}

	return nil
}

type databaseCached struct {
	cachedAt time.Time
	name     string
	url      string
	rows     *sql.Rows
	tx       *sql.Tx
}

func (dc *databaseCached) Name() string {
	return dc.name
}

func (dc *databaseCached) URL() string {
	return dc.url
}

func (dc *databaseCached) Next() (*Post, error) {
	if !dc.rows.Next() {
		if dc.rows.Err() != nil {
			return nil, fmt.Errorf("next: %w", dc.rows.Err())
		}

		return nil, io.EOF
	}

	var post Post
	var tags []byte
	err := dc.rows.Scan(&post.Source, &post.ID, &post.Author, &post.AvatarURL, &post.URL, &post.Title, &post.DescriptionHTML, &tags, &post.DateString, &post.Date)
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}

	err = json.Unmarshal(tags, &post.Tags)
	if err != nil {
		return nil, fmt.Errorf("decode tags: %w", err)
	}

	return &post, nil
}

func (dc *databaseCached) Close() error {
	if dc.tx != nil {
		err := dc.tx.Rollback()
		if err != nil {
			fmt.Printf("close tx: %s\n", err)
		}
	}
	return dc.rows.Close()
}
