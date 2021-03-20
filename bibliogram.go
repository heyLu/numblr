package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var BibliogramInstancesURL = "https://bibliogram.snopyta.org/api/instances"

var bibliogramInstances []string
var bibliogramInitialized bool

func init() {
	rand.Seed(time.Now().UnixNano())
}

// NewBibliogram creates a new feed for Instagram, via Bibliogram.
//
// See https://git.sr.ht/~cadence/bibliogram.
func NewBibliogram(_ context.Context, name string, _ Search) (Feed, error) {
	if !bibliogramInitialized {
		var err error
		bibliogramInstances, err = initBibliogram()
		if err != nil {
			return nil, fmt.Errorf("initializing bibliogram: %w", err)
		}
		bibliogramInitialized = len(bibliogramInstances) > 0
	}

	nameIdx := strings.Index(name, "@")
	rssURL := bibliogramInstances[rand.Intn(len(bibliogramInstances))] + fmt.Sprintf("/u/%s/rss.xml", url.PathEscape(name[:nameIdx]))

	var resp *http.Response
	var err error

	for attempts := 0; attempts < len(bibliogramInstances); attempts++ {
		rssURL = bibliogramInstances[rand.Intn(len(bibliogramInstances))] + fmt.Sprintf("/u/%s/rss.xml", url.PathEscape(name[:nameIdx]))
		resp, err = http.Get(rssURL)
		if err != nil {
			err = fmt.Errorf("download %q: %w", name, err)
			continue
		}
		if resp.StatusCode != 200 {
			err = fmt.Errorf("download %q: wrong response code: %d", rssURL, resp.StatusCode)
			if resp.StatusCode < 500 {
				break
			}
		}

		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, err
	}

	dec := xml.NewDecoder(resp.Body)
	token, err := dec.Token()
	for err == nil {
		el, ok := token.(xml.EndElement)
		if ok && el.Name.Space == "http://www.w3.org/2005/Atom" && el.Name.Local == "link" {
			break
		}
		token, err = dec.Token()
	}
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("skip token: %w", err)
	}

	return &bibliogramRSS{url: rssURL, tumblrRSS: tumblrRSS{name: name, r: resp.Body, dec: dec, dateFormat: NitterDate}}, nil
}

type bibliogramRSS struct {
	url string
	tumblrRSS
}

func (br bibliogramRSS) URL() string {
	return br.url
}

func (br bibliogramRSS) Next() (*Post, error) {
	post, err := br.tumblrRSS.Next()
	if err != nil {
		return nil, err
	}

	post.Source = "instagram"

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

func initBibliogram() ([]string, error) {
	resp, err := http.Get(BibliogramInstancesURL)
	if err != nil {
		return nil, fmt.Errorf("get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d (%s)", resp.StatusCode, http.StatusText(resp.StatusCode))
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
