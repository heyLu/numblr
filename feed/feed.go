package feed

import (
	"errors"
	"fmt"
	"io"
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
	for _, t := range m.feeds {
		if !first {
			name += " "
		}
		first = false
		name += t.Name()
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
