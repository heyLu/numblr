package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3" // use sqlite3 for this feed

	"github.com/heyLu/numblr/feed"
)

// CacheTime is the duration that feeds should be cached for.
var CacheTime time.Duration

// InitDatabase creates a cache database at dbPath and returns a connection to
// it.
func InitDatabase(dbPath string) (*sql.DB, error) {
	if dbPath == "" {
		dbPath = "file::memory:?mode=memory&cache=shared"
	} else if !strings.HasPrefix(dbPath, "file:") {
		dbPath = "file:" + dbPath + "?_journal_mode=WAL&_busy_timeout=50"
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}

	db.SetConnMaxLifetime(60 * time.Minute)
	db.SetMaxIdleConns(100)

	if strings.Contains(dbPath, ":memory:") {
		db.SetConnMaxLifetime(0)
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS feed_infos ( name TEXT PRIMARY KEY, url TEXT, cached_at DATE, description TEXT, error TEXT )`)
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

// ListFeedsOlderThan lists feeds older than time so that they can be updated.
func ListFeedsOlderThan(ctx context.Context, db *sql.DB, olderThan time.Time) ([]string, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}

	rows, err := tx.Query(`SELECT name FROM feed_infos WHERE ? > cached_at ORDER BY RANDOM()`, olderThan)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("select: %w", err)
	}
	defer rows.Close()

	feeds := make([]string, 0, 10)
	for rows.Next() {
		var feed string
		err := rows.Scan(&feed)
		if err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("scan: %w", err)
		}

		feeds = append(feeds, feed)
	}

	if rows.Err() != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("after scan: %w", rows.Err())
	}

	return feeds, tx.Commit()
}

// OpenCached returns a feed that is either already cached or one that will
// cache the uncached in the database one as it is iterated through.
func OpenCached(ctx context.Context, db *sql.DB, name string, uncachedFn feed.Open, search feed.Search) (feed.Feed, error) {
	// FIXME: cache non-canonical names correctly (e.g. oops@tumblr should be looked up as `oops`)
	row := db.QueryRowContext(ctx, "SELECT cached_at, url, description, error FROM feed_infos WHERE name = ?", name)
	var cachedAt time.Time
	var url string
	var description string
	var feedError *string
	err := row.Scan(&cachedAt, &url, &description, &feedError)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("looking up feed: %w", err)
	}

	isCached := err != sql.ErrNoRows

	_, hasTimeout := ctx.Deadline()
	origCtx := ctx
	cancel := func() {}
	if !search.ForceFresh && !hasTimeout && isCached {
		// if we have the feed cached and the uncached one took too long, return the cached one
		ctx, cancel = context.WithTimeout(ctx, 150*time.Millisecond)
	}

	if !search.ForceFresh && (isCached && time.Since(cachedAt) < CacheTime || feedError != nil && *feedError != "") {
		notes := []string{"cached"}

		var rows *sql.Rows
		if search.BeforeID != "" {
			if search.NoReblogs {
				notes = append(notes, "noreblogs")
				rows, err = db.QueryContext(ctx, `SELECT source, id, author, avatar_url, url, title, description_html, tags, date_string, date FROM posts WHERE author = ? AND id < ? AND description_html NOT LIKE '%class="tumblr_blog"%' ORDER BY id DESC LIMIT 20`, name, search.BeforeID)
			} else {
				rows, err = db.QueryContext(ctx, "SELECT source, id, author, avatar_url, url, title, description_html, tags, date_string, date FROM posts WHERE author = ? AND date < (SELECT date FROM posts WHERE author = ? AND  id < ? ORDER BY id DESC) AND id < ? ORDER BY date DESC LIMIT 20", name, name, search.BeforeID, search.BeforeID)
			}
		} else if len(search.Terms) > 0 {
			notes = append(notes, "search")

			match := "%" + search.Terms[0] + "%"
			rows, err = db.QueryContext(ctx, "SELECT source, id, author, avatar_url, url, title, description_html, tags, date_string, date FROM posts WHERE author = ? AND (title LIKE ? OR description_html LIKE ? OR tags LIKE ?) ORDER BY date DESC LIMIT 20", name, match, match, match)
		} else if len(search.Tags) > 0 {
			notes = append(notes, "tags")
			// TODO: support filtering for multiple tags at once
			rows, err = db.QueryContext(ctx, "SELECT source, id, author, avatar_url, url, title, description_html, tags, date_string, date FROM posts WHERE author = ? AND (tags LIKE ?) ORDER BY date DESC LIMIT 20", name, "%"+search.Tags[0]+"%")
		} else {
			if search.NoReblogs {
				notes = append(notes, "noreblogs")
				rows, err = db.QueryContext(ctx, `SELECT source, id, author, avatar_url, url, title, description_html, tags, date_string, date FROM posts WHERE author = ? AND description_html NOT LIKE '%class="tumblr_blog"%' ORDER BY date DESC LIMIT 20`, name)
			} else {
				rows, err = db.QueryContext(ctx, "SELECT source, id, author, avatar_url, url, title, description_html, tags, date_string, date FROM posts WHERE author = ? ORDER BY date DESC LIMIT 20", name)
			}
		}
		if err != nil {
			cancel()
			return nil, fmt.Errorf("querying posts: %w", err)
		}

		if feedError != nil && *feedError != "" {
			notes = []string{fmt.Sprintf("cached-by-error: %s", *feedError)}
		}
		return &databaseCached{name: name, description: description, url: url, rows: rows, cancel: cancel, notes: notes}, nil
	}

	if name == "random" {
		rows, err := db.QueryContext(ctx, "SELECT source, id, author, avatar_url, url, title, description_html, tags, date_string, date FROM posts WHERE author IN (SELECT name FROM feed_infos ORDER BY RANDOM() LIMIT 20) GROUP BY author ORDER BY RANDOM() LIMIT 20", name)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("querying posts: %w", err)
		}

		return &databaseCached{name: name, description: description, url: url, rows: rows, cancel: cancel}, nil

	}

	cancel()

	uncachedFeed, err := uncachedFn(ctx, name, search)
	if err != nil {
		fallbackCtx := origCtx
		cancel := func() {}
		if !search.ForceFresh {
			// give more time for the second try here
			fallbackCtx, cancel = context.WithTimeout(origCtx, 500*time.Millisecond)
		}

		if !search.ForceFresh && isCached && (errors.Is(ctx.Err(), context.DeadlineExceeded) || isTimeoutError(err)) {
			log.Printf("returning out-of-date feed %q, caused by %v / %v", name, ctx.Err(), err)
			var rows *sql.Rows
			var err error
			if search.BeforeID != "" {
				rows, err = db.QueryContext(fallbackCtx, "SELECT source, id, author, avatar_url, url, title, description_html, tags, date_string, date FROM posts WHERE author = ? AND id < ? ORDER BY date DESC LIMIT 20", name, search.BeforeID)
			} else {
				rows, err = db.QueryContext(fallbackCtx, "SELECT source, id, author, avatar_url, url, title, description_html, tags, date_string, date FROM posts WHERE author = ? ORDER BY date DESC LIMIT 20", name)
			}
			if err != nil {
				cancel()
				return nil, fmt.Errorf("querying posts: %w", err)
			}

			return &databaseCached{name: name, description: description, url: url, outOfDate: true, rows: rows, cancel: cancel, notes: []string{"timeout"}}, nil
		}

		// TODO: do not store in table if things don't exist ("no such host")
		// TODO: remove from table if "invalid"?  (difficult to do, don't want to loose valid feeds => check if we have content, let remain if posts exist?)
		_, updateErr := db.ExecContext(fallbackCtx, `INSERT OR REPLACE INTO feed_infos VALUES (?, ?, ?, ?, ?)`, name, url, time.Now(), description, err.Error())
		if updateErr != nil {
			updateErr = fmt.Errorf("update feed_infos after error: %w", updateErr)
			log.Printf("Error: %s", updateErr)
		}

		var statusErr feed.StatusError
		if ok := errors.As(err, &statusErr); ok && statusErr.Code == http.StatusNotFound {
			var rows *sql.Rows
			var err error
			if search.BeforeID != "" {
				rows, err = db.QueryContext(fallbackCtx, "SELECT source, id, author, avatar_url, url, title, description_html, tags, date_string, date FROM posts WHERE author = ? AND id < ? ORDER BY date DESC LIMIT 20", name, search.BeforeID)
			} else {
				rows, err = db.QueryContext(fallbackCtx, "SELECT source, id, author, avatar_url, url, title, description_html, tags, date_string, date FROM posts WHERE author = ? ORDER BY date DESC LIMIT 20", name)
			}
			if err != nil {
				cancel()
				return nil, fmt.Errorf("querying posts: %w", err)
			}

			return &databaseCached{name: name, description: description, url: url, outOfDate: true, rows: rows, cancel: cancel, notes: []string{"not-found"}}, nil
		}

		cancel()
		return nil, fmt.Errorf("open uncached: %w", err)
	}

	return &databaseCaching{
		db:       db,
		uncached: uncachedFeed,
		cachedAt: time.Now(),
		posts:    make([]*feed.Post, 0, 10),
	}, nil
}

func isTimeoutError(err error) bool {
	if strings.Contains(err.Error(), "Temporary failure in name resolution") {
		return true
	}
	if strings.HasSuffix(err.Error(), "i/o timeout") {
		return true
	}
	te, hasTimeout := err.(timeoutError)
	if !hasTimeout {
		return false
	}
	return te.Timeout()
}

type timeoutError interface {
	Timeout() bool
}

type databaseCaching struct {
	db       *sql.DB
	uncached feed.Feed
	cachedAt time.Time
	posts    []*feed.Post
}

func (ct *databaseCaching) Name() string {
	return ct.uncached.Name()
}

func (ct *databaseCaching) Description() string {
	return ct.uncached.Description()
}

func (ct *databaseCaching) URL() string {
	return ct.uncached.URL()
}

func (ct *databaseCaching) Next() (*feed.Post, error) {
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
		closeErr := ct.uncached.Close()
		return fmt.Errorf("saving: %w (closing: %s)", err, closeErr)
	}
	return ct.uncached.Close()
}

func (ct *databaseCaching) Save() error {
	if len(ct.posts) == 0 {
		return nil
	}

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
			_ = tx.Rollback()
			return fmt.Errorf("empty post id: %#v", post)
		}
		if post.Source == "" {
			_ = tx.Rollback()
			return fmt.Errorf("empty post source: %#v", post)
		}

		tagsJSON, err := json.Marshal(post.Tags)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("encode tags: %w", err)
		}

		stmt += "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?), "
		vals = append(vals, post.Source, ct.uncached.Name(), post.ID, post.Author, post.AvatarURL, post.URL, post.Title, post.DescriptionHTML, tagsJSON, post.DateString, post.Date)
	}

	// trim last comma and space
	stmt = stmt[:len(stmt)-2]

	_, err = tx.Exec(stmt, vals...)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("update posts: %w", err)
	}

	res, err := tx.Exec(`INSERT OR REPLACE INTO feed_infos VALUES (?, ?, ?, ?, ?)`, ct.uncached.Name(), ct.uncached.URL(), ct.cachedAt, ct.uncached.Description(), "")
	if err != nil {
		_ = tx.Rollback()
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
	name        string
	description string
	url         string
	outOfDate   bool
	notes       []string
	rows        *sql.Rows
	cancel      func()
	lastPost    *feed.Post
}

func (dc *databaseCached) Name() string {
	if dc.lastPost != nil && dc.lastPost.Author != "" {
		return dc.lastPost.Author
	}
	return dc.name
}

func (dc *databaseCached) Description() string {
	return dc.description
}

func (dc *databaseCached) URL() string {
	return dc.url
}

func (dc *databaseCached) Notes() string {
	return strings.Join(dc.notes, ",")
}

func (dc *databaseCached) Next() (*feed.Post, error) {
	if !dc.rows.Next() {
		if dc.rows.Err() != nil {
			return nil, fmt.Errorf("next: %w", dc.rows.Err())
		}

		return nil, io.EOF
	}

	var post feed.Post
	var tags []byte
	err := dc.rows.Scan(&post.Source, &post.ID, &post.Author, &post.AvatarURL, &post.URL, &post.Title, &post.DescriptionHTML, &tags, &post.DateString, &post.Date)
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}

	err = json.Unmarshal(tags, &post.Tags)
	if err != nil {
		return nil, fmt.Errorf("decode tags: %w", err)
	}

	if dc.outOfDate {
		post.Tags = append(post.Tags, "numblr:out-of-date")
	}

	dc.lastPost = &post
	return &post, nil
}

func (dc *databaseCached) Close() error {
	if dc.cancel != nil {
		dc.cancel()
	}
	return dc.rows.Close()
}
