package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

type Stats struct {
	// mu protects all fields.
	mu sync.Mutex

	NumFeeds  int
	NumPosts  int
	CacheSize int64

	RecentErrors []string
	lastError    int
	seenError    map[string]bool

	RecentUsers []string
	lastUser    int
	seenUser    map[string]bool
}

var globalStats *Stats = nil

func EnableStats(numErrors int, numUsers int) {
	globalStats = &Stats{}
	globalStats.RecentErrors = make([]string, numErrors)
	globalStats.seenError = make(map[string]bool, numUsers)
	globalStats.RecentUsers = make([]string, numUsers)
	globalStats.seenUser = make(map[string]bool, numUsers)
}

func EnableDatabaseStats(db *sql.DB, path string) {
	go func() {
		for {
			err := func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
				defer cancel()
				row := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM feed_infos")
				err := row.Scan(&globalStats.NumFeeds)
				if err != nil {
					return err
				}

				ctx, cancel = context.WithTimeout(context.Background(), 1*time.Second)
				defer cancel()
				row = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM posts")
				err = row.Scan(&globalStats.NumPosts)
				if err != nil {
					return err
				}

				fi, err := os.Lstat(path)
				if err != nil {
					return err
				}
				globalStats.CacheSize = fi.Size()

				return nil
			}()
			if err != nil {
				CollectError(err)
				log.Printf("collecting db stats: %s", err)
			}

			time.Sleep(1 * time.Minute)
		}
	}()
}

func CollectError(err error) {
	if globalStats == nil {
		return
	}

	if err == nil {
		return
	}

	s := err.Error()

	globalStats.mu.Lock()
	defer globalStats.mu.Unlock()
	if globalStats.seenError[s] {
		return
	}
	globalStats.seenError[s] = true
	oldestError := (globalStats.lastError + 1) % len(globalStats.RecentErrors)
	delete(globalStats.seenError, globalStats.RecentErrors[oldestError])
	globalStats.RecentErrors[globalStats.lastError%len(globalStats.RecentErrors)] = s
	globalStats.lastError = oldestError
}

func CollectUser(s string) {
	if globalStats == nil {
		return
	}

	globalStats.mu.Lock()
	defer globalStats.mu.Unlock()
	if globalStats.seenUser[s] {
		return
	}
	globalStats.seenUser[s] = true
	oldestUser := (globalStats.lastUser + 1) % len(globalStats.RecentUsers)
	delete(globalStats.seenUser, globalStats.RecentUsers[oldestUser])
	globalStats.RecentUsers[globalStats.lastUser%len(globalStats.RecentUsers)] = s
	globalStats.lastUser = oldestUser
}

func StatsHandler(w http.ResponseWriter, req *http.Request) {
	if globalStats == nil {
		w.Write([]byte("stats not enabled"))
		return
	}

	fmt.Fprintf(w, "feeds: %d\n", globalStats.NumFeeds)
	fmt.Fprintf(w, "posts: %d\n", globalStats.NumPosts)
	fmt.Fprintf(w, "cache: %s\n", Bytes(globalStats.CacheSize))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "recent errors:")
	for _, err := range globalStats.RecentErrors {
		if err != "" {
			fmt.Fprintln(w, "  ", err)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "recent users:")
	for _, user := range globalStats.RecentUsers {
		if user != "" {
			fmt.Fprintln(w, " ", user)
		}
	}
}

type Bytes int64

func (b Bytes) String() string {
	switch {
	case b > 1024*1024*1024:
		return fmt.Sprintf("%.2fgb", float32(b)/1024/1024/1024)
	case b > 1024*1024:
		return fmt.Sprintf("%.2fmb", float32(b)/1024/1024)
	case b > 1024:
		return fmt.Sprintf("%dkb", b/1024)
	default:
		return fmt.Sprintf("%db", b)
	}
}
