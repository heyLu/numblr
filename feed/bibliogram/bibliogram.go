package bibliogram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/heyLu/numblr/feed"
	"github.com/heyLu/numblr/feed/rss"
)

// BibliogramInstancesURL is the url used to discover available Bibliogram instances.
var BibliogramInstancesURL = "https://bibliogram.art/api/instances"

var bibliogramInstances []string
var bibliogramInitialized bool

func init() {
	rand.Seed(time.Now().UnixNano())
}

// Open creates a new feed for Instagram, via Bibliogram.
//
// See https://git.sr.ht/~cadence/bibliogram.
func Open(ctx context.Context, name string, search feed.Search) (feed.Feed, error) {
	if !bibliogramInitialized {
		var err error
		bibliogramInstances, err = initBibliogram(ctx)
		if err != nil {
			return nil, fmt.Errorf("initializing bibliogram: %w", err)
		}
		bibliogramInitialized = len(bibliogramInstances) > 0
	}

	nameIdx := strings.Index(name, "@")
	var rssURL string

	var rssFeed feed.Feed
	var err error

	for attempts := 0; attempts < len(bibliogramInstances); attempts++ {
		rssURL = bibliogramInstances[rand.Intn(len(bibliogramInstances))] + fmt.Sprintf("/u/%s/rss.xml", url.PathEscape(name[:nameIdx]))

		rssFeed, err = rss.Open(ctx, rssURL, search)
		if err != nil {
			var statusErr feed.StatusError
			if ok := errors.As(err, &statusErr); ok {
				if statusErr.Code == http.StatusForbidden {
					continue
				}
				if statusErr.Code < http.StatusInternalServerError {
					break
				}
			} else {
				err = fmt.Errorf("download %q: %w", name, err)
				continue
			}
		}

		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, err
	}

	return &bibliogramRSS{name: name[:nameIdx] + "@instagram", url: rssURL, Feed: rssFeed}, nil
}

type bibliogramRSS struct {
	name string
	url  string

	feed.Feed
}

func (br bibliogramRSS) Name() string {
	return br.name
}

func (br bibliogramRSS) URL() string {
	return br.url
}

func (br bibliogramRSS) Next() (*feed.Post, error) {
	post, err := br.Feed.Next()
	if err != nil {
		return nil, err
	}

	post.Source = "instagram"

	post.Author = br.name

	post.Title = ""

	baseURL, err := url.Parse(br.url)
	if err == nil {
		postURL, err := baseURL.Parse(post.URL)
		if err == nil {
			post.URL = postURL.String()
		}
	}

	return post, err
}

func initBibliogram(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", BibliogramInstancesURL, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, feed.StatusError{Code: resp.StatusCode}
	}

	var instanceInfo struct {
		Data []struct {
			Address    string `json:"address"`
			RSSEnabled bool   `json:"rss_enabled"`
		} `json:"data"`
	}

	dec := json.NewDecoder(resp.Body)
	err = dec.Decode(&instanceInfo)
	if err != nil {
		return nil, fmt.Errorf("parse instances: %w", err)
	}

	instances := make([]string, 0, len(instanceInfo.Data))
	for _, instance := range instanceInfo.Data {
		if instance.RSSEnabled {
			instances = append(instances, instance.Address)
		}
	}

	return instances, nil
}
