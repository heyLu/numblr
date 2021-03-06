package main

import (
	"fmt"
	"net/url"
	"strings"
)

const NitterDate = "Mon, 2 Jan 2006 15:04:05 MST"

var NitterURL = "https://nitter.net"

// NewNitter creates a new feed for Twitter, via Nitter.
//
// See https://github.com/zedeus/nitter.
func NewNitter(name string, search Search) (Feed, error) {
	nameIdx := strings.Index(name, "@")
	rssURL := fmt.Sprintf("%s/%s/rss", NitterURL, name[:nameIdx])
	if strings.HasPrefix(name[:nameIdx], "#") {
		rssURL = fmt.Sprintf("%s/search?q=%s", NitterURL, url.QueryEscape(name[:nameIdx]))
	}

	feed, err := NewRSS(rssURL, search)
	if err != nil {
		return nil, err
	}

	return &nitterRSS{name: name, rss: feed.(*rss)}, nil
}

type nitterRSS struct {
	name string

	*rss
}

func (nr *nitterRSS) Name() string {
	return nr.name
}

func (nr *nitterRSS) URL() string {
	nameIdx := strings.Index(nr.name, "@")
	return fmt.Sprintf("%s/%s/rss", NitterURL, nr.name[:nameIdx])
}

func (nr *nitterRSS) Next() (*Post, error) {
	post, err := nr.rss.Next()
	if err != nil {
		return nil, err
	}

	// skip pinned posts as they mess up post sorting (for now)
	if strings.HasPrefix(post.Title, "<h1>Pinned: ") {
		return nr.rss.Next()
	}

	post.Source = "twitter"
	post.Author = nr.name

	return post, nil
}
