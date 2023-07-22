package database

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heyLu/numblr/feed"
)

func TestIsTimeoutError(t *testing.T) {
	testCases := []struct {
		err                error
		expectTimeoutError bool
	}{
		{&net.DNSError{IsTimeout: true}, true},
		{fmt.Errorf("random error"), false},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%#v", tc.err), func(t *testing.T) {
			isTimeout := isTimeoutError(tc.err)
			if tc.expectTimeoutError != isTimeout {
				t.Errorf("expected to be %v but was %v", tc.expectTimeoutError, isTimeout)
			}
		})
	}
}

func TestConcurrentWrites(t *testing.T) {
	dbDir := t.TempDir()
	db, err := InitDatabase(path.Join(dbDir, "cache.db"))
	require.NoError(t, err)
	defer db.Close()

	feeds := []string{"staff", "engineering", "changes", "love", "fun", "transtrucks"}

	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))

	var wg sync.WaitGroup
	fetchFeeds := func(feedName string) {
		defer wg.Done()

		if feedName == "" {
			feedName = feeds[rnd.Intn(len(feeds))]
		}

		start := time.Now()
		feed, err := OpenCached(context.Background(), db, feedName, fakeOpen, feed.Search{ForceFresh: true})
		require.NoError(t, err)

		defer feed.Close()

		n := 0
		_, err = feed.Next()
		for err == nil {
			n++
			_, err = feed.Next()
		}
		t.Log(feedName, n, time.Since(start))
		if err != nil && !errors.Is(err, io.EOF) {
			require.NoError(t, err)
		}

		defer time.Sleep(time.Duration(rnd.Intn(100)) * time.Millisecond)
	}

	// warm up
	// for i := 0; i < len(feeds); i++ {
	// 	wg.Add(1)
	// 	fetchFeeds(feeds[i])
	// }
	// t.Log("warmup done")

	go func() {
		for {
			// "TRUNCATE" causes "database is locked" errors, "PASSIVE" does not
			_, err := db.ExecContext(context.Background(), "PRAGMA wal_checkpoint(PASSIVE)")
			if err != nil {
				continue
			}
		}
	}()

	// concurrent
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go fetchFeeds("")
	}

	wg.Wait()
}

func fakeOpen(ctx context.Context, name string, _ feed.Search) (feed.Feed, error) {
	time.Sleep(100 * time.Millisecond)
	return &feed.Static{Posts: []feed.Post{
		{ID: "xyz", Author: name},
	}}, nil
}
