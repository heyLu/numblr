package nitter

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/heyLu/numblr/feed"
	"github.com/heyLu/numblr/feed/rss"
)

// NitterURL is the nitter instance to use to fetch twitter feeds.
var NitterURL = "https://nitter.net"

// Open creates a new feed for Twitter, via Nitter.
//
// See https://github.com/zedeus/nitter.
func Open(ctx context.Context, name string, search feed.Search) (feed.Feed, error) {
	nameIdx := strings.Index(name, "@")
	rssURL := fmt.Sprintf("%s/%s/rss", NitterURL, name[:nameIdx])
	if strings.HasPrefix(name[:nameIdx], "#") {
		rssURL = fmt.Sprintf("%s/search?q=%s", NitterURL, url.QueryEscape(name[:nameIdx]))
	}

	feed, err := rss.Open(ctx, rssURL, search)
	if err != nil {
		return nil, err
	}

	return &nitterRSS{name: name, RSS: feed.(*rss.RSS)}, nil
}

type nitterRSS struct {
	name string

	*rss.RSS
}

func (nr *nitterRSS) Name() string {
	return nr.name
}

func (nr *nitterRSS) URL() string {
	nameIdx := strings.Index(nr.name, "@")
	return fmt.Sprintf("%s/%s/rss", NitterURL, nr.name[:nameIdx])
}

func (nr *nitterRSS) Next() (*feed.Post, error) {
	post, err := nr.RSS.Next()
	if err != nil {
		return nil, err
	}

	// skip pinned posts as they mess up post sorting (for now)
	if strings.HasPrefix(post.Title, "<h1>Pinned: ") {
		return nr.RSS.Next()
	}

	// TODO: render nitter posts nicer

	post.Source = "twitter"
	post.Author = nr.name

	return post, nil
}
