package feed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Feed represent of feed of posts from a given source.
type Feed interface {
	Name() string
	Description() string
	URL() string
	// Next returns the next post in the feed.
	//
	// A feed typically models an existing resource, e.g. posts from an RSS feed
	// or posts from a database that is then iterated over using `Next`.
	//
	// An error matching errors.Is(err, io.EOF) indicates that there are no
	// more posts.
	Next() (*Post, error)
	Close() error
}

// Notes is an extension that Feeds might implement, which add arbitrary notes
// to a feed.
//
// They are currently used to add debugging info to a feed, e.g. if there was
// an error while updating it or if it is cached.
type Notes interface {
	Notes() string
}

// Open is a function that opens a feed identified by `name`.
//
// All feeds currently implement this.
type Open func(ctx context.Context, name string, search Search) (Feed, error)

// OpenCached is a function that caches the feed identified by `name`.
//
// database.OpenCached is currently the only implementation, backed by sqlite.
type OpenCached func(ctx context.Context, name string, uncached Open, search Search) (Feed, error)

// Post is a single post, e.g. a blog post or a tweet.
type Post struct {
	Source          string
	ID              string `xml:"guid"`
	Author          string
	AvatarURL       string
	URL             string   `xml:"link"`
	Title           string   `xml:"title"`
	DescriptionHTML string   `xml:"description"`
	Tags            []string `xml:"category"`
	DateString      string   `xml:"pubDate"`
	Date            time.Time
}

var isReblogRE = regexp.MustCompile(`^\s*[-\w]+:`)

// IsReblog returns true if the post is a repost of another post, likely from
// another source.
func (p Post) IsReblog() bool {
	return isReblogRE.MatchString(p.Title) || strings.Contains(p.DescriptionHTML, `class="tumblr_blog"`)
}

// Merge returns a special feed that merges the posts from the feeds and
// presents them as a single Feed to iterate over.
//
// Each feed is assumed to be sorted already (by date descending).  Merge only
// takes care to preserve the order that exists and returns the posts from all
// feeds in order.
func Merge(feeds ...Feed) Feed {
	return &merger{feeds: feeds, posts: make([]*Post, len(feeds)), errors: make([]error, len(feeds))}
}

type merger struct {
	feeds  []Feed
	posts  []*Post
	errors []error
}

func (m *merger) Name() string {
	name := ""
	first := true
	seen := make(map[string]bool, len(m.feeds))
	for _, t := range m.feeds {
		if seen[t.Name()] {
			continue
		}
		if !first {
			name += " "
		}
		first = false
		name += t.Name()
		seen[t.Name()] = true
	}
	return name
}

func (m *merger) Description() string {
	return ""
}

func (m *merger) URL() string {
	return ""
}

func (m *merger) Next() (*Post, error) {
	allErrors := false
	for _, err := range m.errors {
		allErrors = allErrors && err != nil
	}
	if allErrors {
		return nil, m.errors[0]
	}

	var wg sync.WaitGroup
	wg.Add(len(m.feeds))
	for i := range m.feeds {
		go func(i int) {
			if m.posts[i] == nil && !errors.Is(m.errors[i], io.EOF) {
				m.posts[i], m.errors[i] = m.feeds[i].Next()
			}
			wg.Done()
		}(i)
	}
	wg.Wait()

	postIdx := -1
	var firstPost *Post

	for i, post := range m.posts {
		if post == nil {
			continue
		}

		if firstPost == nil || post.Date.After(firstPost.Date) {
			postIdx = i
			firstPost = post
		}
	}

	if firstPost == nil {
		return nil, fmt.Errorf("no more posts: %w", io.EOF)
	}

	m.posts[postIdx] = nil
	firstPost.Author = m.feeds[postIdx].Name()
	return firstPost, nil
}

func (m *merger) Close() error {
	var err error
	for _, t := range m.feeds {
		err = t.Close()
	}
	return err
}

var _ Feed = &Static{}

// Static is a feed that contains exactly the Posts specified.
type Static struct {
	FeedName        string
	FeedURL         string
	FeedDescription string
	Posts           []Post
}

// Name implements Feed.Name
func (s *Static) Name() string {
	return s.FeedName
}

// Description implements Feed.Description
func (s *Static) Description() string {
	return s.FeedDescription
}

// URL implements Feed.URL
func (s *Static) URL() string {
	return s.FeedURL
}

// Next implements Feed.Next.
func (s *Static) Next() (*Post, error) {
	if len(s.Posts) == 0 {
		return nil, io.EOF
	}

	post := s.Posts[0]
	s.Posts = s.Posts[1:]

	return &post, nil
}

// Close implements Feed.Close.
func (s *Static) Close() error {
	return nil
}

var _ error = StatusError{}

// StatusError is an error with an HTTP status code.
type StatusError struct {
	Code int
}

func (se StatusError) Error() string {
	return fmt.Sprintf("unexpected status code: %d (%s)", se.Code, http.StatusText(se.Code))
}
