package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/andybalholm/cascadia"
	"github.com/mmcdole/gofeed"
	"golang.org/x/net/html"
)

var RelAlternateMatcher = cascadia.MustCompile(`link[rel=alternate]`)

func NewRSS(name string) (Tumblr, error) {
	url := name
	if strings.Contains(name, "@") {
		parts := strings.SplitN(name, "@", 2)
		if len(parts) == 0 {
			return nil, fmt.Errorf("unrecognized feed %q", name)
		}
		url = parts[1] + "/@" + parts[0]
	}

	resp, err := http.Get("http://" + url)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer resp.Body.Close()

	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading: %w", err)
	}
	r := strings.NewReader(buf.String())

	feedType := gofeed.DetectFeedType(r)
	_, err = r.Seek(0, io.SeekStart)
	if err != nil {
		return nil, fmt.Errorf("rewind: %w", err)
	}

	if feedType == gofeed.FeedTypeUnknown {
		node, err := html.Parse(r)
		if err != nil {
			return nil, fmt.Errorf("parse html: %w", err)
		}

		nodes := cascadia.QueryAll(node, RelAlternateMatcher)
		found := false
		var url string
		for _, alternate := range nodes {
			if hasAttribute(alternate, "type", "application/atom+xml") || hasAttribute(alternate, "type", "application/rss+xml") {
				found = true
				for _, attr := range alternate.Attr {
					if attr.Key == "href" {
						url = attr.Val
						break
					}
				}
				break
			}
		}

		if !found {
			return nil, fmt.Errorf("no feed found")
		}

		if !strings.HasPrefix(url, "http") {
			url = "http://" + path.Join(name, url)
		}

		resp, err := http.Get(url)
		if err != nil {
			return nil, fmt.Errorf("open feed: %w", err)
		}
		defer resp.Body.Close()

		buf := new(bytes.Buffer)
		_, err = io.Copy(buf, resp.Body)
		if err != nil {
			return nil, fmt.Errorf("reading: %w", err)
		}

		r = strings.NewReader(buf.String())
	}

	parser := gofeed.NewParser()
	feed, err := parser.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	return &rss{name: name, feed: feed}, nil
}

type rss struct {
	name string
	feed *gofeed.Feed
}

func (rss *rss) Name() string {
	return rss.name
}

func (rss *rss) URL() string {
	return rss.feed.Link
}

func (rss *rss) Next() (*Post, error) {
	if len(rss.feed.Items) == 0 {
		return nil, io.EOF
	}

	item := rss.feed.Items[0]
	rss.feed.Items = rss.feed.Items[1:]

	var avatarURL string
	if rss.feed.Image != nil {
		avatarURL = rss.feed.Image.URL
	}

	dateString := item.Published
	date := item.PublishedParsed
	if date == nil {
		dateString = item.Updated
		date = item.UpdatedParsed
	}
	if date == nil {
		t := time.Now()
		date = &t
		dateString = date.UTC().Format(time.RFC3339)
	}
	content := item.Content
	if content == "" {
		content = item.Description
	}
	for _, encl := range item.Enclosures {
		if strings.HasPrefix(encl.Type, "image") {
			content += fmt.Sprintf(`<img src="%s" />`, encl.URL)
		}
	}
	return &Post{
		Author:          rss.name,
		AvatarURL:       avatarURL,
		URL:             item.Link,
		Title:           fmt.Sprintf(`<h1>%s</h1>`, item.Title),
		DescriptionHTML: content,
		Tags:            item.Categories,
		DateString:      dateString,
		Date:            *date,
	}, nil
}

func (rss *rss) Close() error {
	return nil
}
