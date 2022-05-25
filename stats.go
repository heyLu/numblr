package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

type Stats struct {
	// mu protects all fields.
	mu sync.Mutex

	NumViews  int
	NumFeeds  int
	NumPosts  int
	CacheSize int64
	DBStats   sql.DBStats

	RecentErrors []string
	lastError    int
	seenError    map[string]int

	RecentUsers []string
	lastUser    int
	seenUser    map[string]int

	RecentLogs []string
	lastLog    int
	seenLog    map[string]int
}

var globalStats *Stats = nil

func EnableStats(numErrors int, numUsers int, numLogs int) {
	globalStats = &Stats{}
	globalStats.RecentErrors = make([]string, numErrors)
	globalStats.seenError = make(map[string]int, numUsers)
	globalStats.RecentUsers = make([]string, numUsers)
	globalStats.seenUser = make(map[string]int, numUsers)
	globalStats.RecentLogs = make([]string, numLogs)
	globalStats.seenLog = make(map[string]int, numLogs)
}

func EnableDatabaseStats(db *sql.DB, path string) {
	go func() {
		for {
			err := func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
				defer cancel()

				fi, err := os.Lstat(path)
				if err != nil {
					return err
				}
				globalStats.CacheSize = fi.Size()

				globalStats.DBStats = db.Stats()

				row := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM feed_infos")
				err = row.Scan(&globalStats.NumFeeds)
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

func CountView() {
	if globalStats == nil {
		return
	}

	globalStats.mu.Lock()
	globalStats.NumViews++
	globalStats.mu.Unlock()
}

type CollectLogsWriter struct{}

func (clw *CollectLogsWriter) Write(p []byte) (n int, err error) {
	n = len(p)
	err = nil

	if globalStats == nil {
		return
	}

	s := string(p[:len(p)-1])

	globalStats.mu.Lock()
	defer globalStats.mu.Unlock()

	// skip logs that have been logged as errors before
	for seenErr := range globalStats.seenError {
		if strings.HasSuffix(s, seenErr) {
			return
		}
	}

	if globalStats.seenLog[s] > 0 {
		globalStats.seenLog[s]++
		return
	}
	globalStats.seenLog[s]++
	oldestLog := (globalStats.lastLog + 1) % len(globalStats.RecentLogs)
	delete(globalStats.seenLog, globalStats.RecentLogs[oldestLog])
	globalStats.RecentLogs[globalStats.lastLog%len(globalStats.RecentLogs)] = s
	globalStats.lastLog = oldestLog

	return
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
	if globalStats.seenError[s] > 0 {
		globalStats.seenError[s]++
		return
	}
	globalStats.seenError[s]++
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
	if globalStats.seenUser[s] > 0 {
		globalStats.seenUser[s]++
		return
	}
	globalStats.seenUser[s]++
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
	fmt.Fprintf(w, "views: %d\n", globalStats.NumViews)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "db:")
	fmt.Fprintf(w, "  max:         %d\n", globalStats.DBStats.MaxOpenConnections)
	fmt.Fprintf(w, "  conns:       %d\n", globalStats.DBStats.OpenConnections)
	fmt.Fprintf(w, "  in-use:      %d\n", globalStats.DBStats.InUse)
	fmt.Fprintf(w, "  idle:        %d\n", globalStats.DBStats.Idle)
	fmt.Fprintf(w, "  wait-count:  %d\n", globalStats.DBStats.WaitCount)
	fmt.Fprintf(w, "  wait-dur:    %s\n", globalStats.DBStats.WaitDuration)
	fmt.Fprintf(w, "  idle-closed: %d\n", globalStats.DBStats.MaxIdleTimeClosed)
	fmt.Fprintf(w, "  max-closed:  %d\n", globalStats.DBStats.MaxIdleClosed)
	fmt.Fprintf(w, "  old-closed:  %d\n", globalStats.DBStats.MaxLifetimeClosed)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "recent errors:")
	for _, err := range globalStats.RecentErrors {
		if err != "" {
			fmt.Fprintf(w, "  %s (%d)\n", err, globalStats.seenError[err])
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "recent users:")
	for _, user := range globalStats.RecentUsers {
		if user != "" {
			fmt.Fprintf(w, "  %s (%d)\n", user, globalStats.seenUser[user])
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "recent logs:")
	for _, log := range globalStats.RecentLogs {
		if log != "" {
			fmt.Fprintf(w, "  %s (%d)\n", log, globalStats.seenLog[log])
		}
	}
	fmt.Fprintln(w)
	version := "unknown (missing build info)"
	buildInfo, ok := debug.ReadBuildInfo()
	if ok {
		version = buildInfo.String()
	}
	fmt.Fprintln(w, "build info:")
	fmt.Fprintln(w)
	fmt.Fprintln(w, version)
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
