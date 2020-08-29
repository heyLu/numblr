package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"time"
)

const NitterDate = "Mon, 2 Jan 2006 15:04:05 MST"

func NewNitter(name string) (Tumblr, error) {
	rssURL := fmt.Sprintf("https://nitter.net/%s/rss", name)
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

	return &nitterRSS{name: name, r: resp.Body, dec: dec}, nil
}

type nitterRSS struct {
	name string
	r    io.ReadCloser
	dec  *xml.Decoder
}

func (nr *nitterRSS) Name() string {
	return nr.name
}

func (nr *nitterRSS) URL() string {
	return fmt.Sprintf("https://nitter.net/%s/rss", nr.name)
}

func (nr *nitterRSS) Next() (*Post, error) {
	var post Post
	err := nr.dec.Decode(&post)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	post.Author = nr.name

	t, dateErr := time.Parse(NitterDate, post.DateString)
	if dateErr != nil {
		return nil, fmt.Errorf("invalid date %q: %s", post.DateString, dateErr)
	}
	post.Date = t

	return &post, err
}

func (nr *nitterRSS) Close() error {
	return nr.r.Close()
}
