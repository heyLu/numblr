package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// maxResultSize is the maximum amount of bytes to read from a YouTube
// search result page.
const maxResultSize = 300 * 1000 * 1000

var searchResultStart = []byte(`{"primaryContents":{"sectionListRenderer":{"contents":[{"itemSectionRenderer":{"contents":`)

// NewYoutube creates a new feed for YouTube.
func NewYoutube(ctx context.Context, name string, search Search) (Feed, error) {
	nameIdx := strings.Index(name, "@")

	name = name[:nameIdx]
	searchURL := "https://www.youtube.com/results?search_query=" + url.QueryEscape(name) + "&sp=EgIQAg%253D%253D"

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searching youtube: %w", err)
	}
	defer resp.Body.Close()

	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, &io.LimitedReader{R: resp.Body, N: maxResultSize})
	if err != nil {
		return nil, fmt.Errorf("reading search results: %w", err)
	}

	content := buf.Bytes()
	searchResultsIdx := bytes.Index(content, searchResultStart)
	if searchResultsIdx == -1 {
		return nil, fmt.Errorf("invalid search results: %q not found", searchResultStart)
	}

	buf.Reset()
	_, err = buf.Write(content[searchResultsIdx+len(searchResultStart):])
	if err != nil {
		return nil, fmt.Errorf("truncating search results: %w", err)
	}

	var results []youtubeChannel
	dec := json.NewDecoder(buf)
	err = dec.Decode(&results)
	if err != nil {
		return nil, fmt.Errorf("parsing search results: %w", err)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("no channel %q found", name)
	}

	channelID := results[0].ChannelRenderer.ChannelID
	if channelID == "" {
		return nil, fmt.Errorf("no channel %q found (empty channel id)", name)
	}

	baseURL, _ := url.Parse("https://www.youtube.com")
	channelURL, err := url.Parse(results[0].ChannelRenderer.NavigationEndpoint.BrowseEndpoint.CanonicalBaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid channel url: %w", err)
	}
	channelURL = baseURL.ResolveReference(channelURL)

	var avatarURL string
	thumbnails := results[0].ChannelRenderer.Thumbnail.Thumbnails
	if len(thumbnails) > 0 {
		thumbnailURL, err := url.Parse(thumbnails[len(thumbnails)-1].URL)
		if err != nil {
			return nil, fmt.Errorf("invalid thumbnail url: %w", err)
		}
		avatarURL = baseURL.ResolveReference(thumbnailURL).String()
	}

	// TODO: parse community posts (?)

	feed, err := NewRSS(ctx, "https://www.youtube.com/feeds/videos.xml?channel_id="+url.QueryEscape(channelID), search)
	if err != nil {
		return nil, err
	}

	return &youtubeRSS{name: name, url: channelURL.String(), avatarURL: avatarURL, rss: feed.(*rss)}, nil
}

type youtubeRSS struct {
	name      string
	url       string
	avatarURL string

	*rss
}

func (yt *youtubeRSS) Name() string {
	return yt.name + "@youtube"
}

func (yt *youtubeRSS) URL() string {
	fmt.Println("should output the url...", yt.url)
	return yt.url
}

func (yt *youtubeRSS) Next() (*Post, error) {
	post, err := yt.rss.Next()
	if err != nil {
		return nil, err
	}

	post.Source = "youtube"
	post.Author = yt.name

	post.AvatarURL = yt.avatarURL

	post.DescriptionHTML = ""

	thumbnail := yt.FeedItem().Extensions["media"]["group"][0].Children["thumbnail"][0].Attrs["url"]
	post.DescriptionHTML += fmt.Sprintf("<p><a href=%q><img src=%q /></a></p>", post.URL, thumbnail)

	description := yt.FeedItem().Extensions["media"]["group"][0].Children["description"][0].Value
	description = strings.ReplaceAll(description, "\n\n", "<p>")
	description = strings.ReplaceAll(description, "\n", "<br />")
	post.DescriptionHTML += description

	return post, nil
}

// youtubeChannel is the internal JSON format that YouTube uses to
// render channels on their website.
//
// {
//   "channelRenderer": {
//     "channelId": "UCMb0O2CdPBNi-QqPk5T3gsQ",
//     "title": {
//       "simpleText": "James Hoffmann"
//     },
//     "navigationEndpoint": {
//       "browseEndpoint": {
//         "canonicalBaseUrl": "/channel/UCMb0O2CdPBNi-QqPk5T3gsQ"
//       }
//     },
//     "thumbnail": {
//       "thumbnails": [
//         {
//           "url": "//yt3.ggpht.com/ytc/AKedOLSI1ZkDWLUAadY8yZgw8psx3RSCEL09hHfND9gN=s88-c-k-c0x00ffffff-no-rj-mo",
//           "width": 88,
//           "height": 88
//         },
//         {
//           "url": "//yt3.ggpht.com/ytc/AKedOLSI1ZkDWLUAadY8yZgw8psx3RSCEL09hHfND9gN=s176-c-k-c0x00ffffff-no-rj-mo",
//           "width": 176,
//           "height": 176
//         }
//       ]
//     },
//     "descriptionSnippet": {
//       "runs": [
//         { "text": "Hi! My name is " },
//         { "text": "James", "bold": true },
//         { "text": ", and I mostly make videos about anything and everything to do with coffee, occasionally food and&Acirc;&nbsp;..." }
//       ]
//     },
//   }
// }
type youtubeChannel struct {
	ChannelRenderer struct {
		ChannelID string `json:"channelId"`
		Title     struct {
			SimpleText string `json:"simpleText"`
		} `json:"title"`
		NavigationEndpoint struct {
			BrowseEndpoint struct {
				CanonicalBaseURL string `json:"canonicalBaseUrl"`
			} `json:"browseEndpoint"`
		} `json:"navigationEndpoint"`
		Thumbnail struct {
			Thumbnails []struct {
				URL    string `json:"url"`
				Width  int    `json:"width"`
				Height int    `json:"height"`
			} `json:"thumbnails"`
		} `json:"thumbnail"`
		DescriptionSnippet struct {
			Runs []struct {
				Text string `json:"text"`
			}
		} `json:"descriptionSnippet"`
	} `json:"channelRenderer"`
}
