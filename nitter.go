package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const NitterDate = "Mon, 2 Jan 2006 15:04:05 MST"

// NewNitter creates a new feed for Twitter, via Nitter.
//
// See https://github.com/zedeus/nitter.
func NewNitter(name string) (Tumblr, error) {
	nameIdx := strings.Index(name, "@")
	rssURL := fmt.Sprintf("https://nitter.net/%s/rss", name[:nameIdx])
	resp, err := http.Get(rssURL)
	if err != nil {
		return nil, fmt.Errorf("download %q: %w", name, err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download %q: wrong response code: %d", rssURL, resp.StatusCode)
	}

	dec := xml.NewDecoder(resp.Body)
	token, err := dec.Token()
	for err == nil {
		el, ok := token.(xml.EndElement)
		if ok && el.Name.Local == "image" {
			break
		}
		token, err = dec.Token()
	}
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("skip token: %w", err)
	}

	return &nitterRSS{tumblrRSS{name: name, r: resp.Body, dec: dec, dateFormat: NitterDate}}, nil
}

type nitterRSS struct {
	tumblrRSS
}

func (nr *nitterRSS) URL() string {
	nameIdx := strings.Index(nr.name, "@")
	return fmt.Sprintf("https://nitter.net/%s/rss", nr.name[:nameIdx])
}
