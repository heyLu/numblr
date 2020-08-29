package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// NewBibliogram creates a new feed for Instagram, via Bibliogram.
//
// See https://git.sr.ht/~cadence/bibliogram.
func NewBibliogram(name string) (Tumblr, error) {
	nameIdx := strings.Index(name, "@")
	rssURL := fmt.Sprintf("https://bibliogram.snopyta.org/u/%s/rss.xml", name[:nameIdx])
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
		if ok && el.Name.Space == "http://www.w3.org/2005/Atom" && el.Name.Local == "link" {
			break
		}
		token, err = dec.Token()
	}
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("skip token: %w", err)
	}

	return &bibliogramRSS{tumblrRSS{name: name, r: resp.Body, dec: dec, dateFormat: NitterDate}}, nil
}

type bibliogramRSS struct {
	tumblrRSS
}

func (br bibliogramRSS) URL() string {
	return fmt.Sprintf("https://bibliogram.snopyta.org/u/%s/rss.xml", br.name)
}
