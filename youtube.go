package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/heyLu/numblr/feed"
	"github.com/heyLu/numblr/search"
)

// maxResultSize is the maximum amount of bytes to read from a YouTube
// search result page.
const maxResultSize = 300 * 1000 * 1000

var searchResultStart = []byte(`{"primaryContents":{"sectionListRenderer":{"contents":[{"itemSectionRenderer":{"contents":`)

// NewYoutube creates a new feed for YouTube.
func NewYoutube(ctx context.Context, name string, search search.Search) (feed.Feed, error) {
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

	req, err = http.NewRequestWithContext(ctx, "GET", "https://youtube.com/channel/"+url.QueryEscape(channelID)+"/community", nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Accept-Language", "en-UK")

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching community posts: %w", err)
	}
	defer resp.Body.Close()

	communityPosts, err := parseCommunityPosts(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parsing community posts: %w", err)
	}

	feed, err := NewRSS(ctx, "https://www.youtube.com/feeds/videos.xml?channel_id="+url.QueryEscape(channelID), search)
	if err != nil {
		return nil, err
	}

	return &youtubeRSS{name: name, url: channelURL.String(), avatarURL: avatarURL, communityPosts: communityPosts, rss: feed.(*rss)}, nil
}

type youtubeRSS struct {
	name      string
	url       string
	avatarURL string

	communityPosts []*feed.Post

	*rss
}

func (yt *youtubeRSS) Name() string {
	return yt.name + "@youtube"
}

func (yt *youtubeRSS) URL() string {
	fmt.Println("should output the url...", yt.url)
	return yt.url
}

func (yt *youtubeRSS) Next() (*feed.Post, error) {
	// TODO: sort community posts correctly (simply merge feeds?)
	if len(yt.communityPosts) > 0 {
		post := yt.communityPosts[0]
		yt.communityPosts = yt.communityPosts[1:]

		post.Source = "youtube"
		post.Author = yt.name

		post.AvatarURL = yt.avatarURL

		return post, nil
	}

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

func parseCommunityPosts(r io.Reader) ([]*feed.Post, error) {
	buf := new(bytes.Buffer)
	_, err := io.Copy(buf, &io.LimitedReader{R: r, N: maxResultSize})
	if err != nil {
		return nil, fmt.Errorf("reading search results: %w", err)
	}

	content := buf.Bytes()
	communityPostsIdx := bytes.Index(content, youtubeCommunityPostsStart)
	if communityPostsIdx == -1 {
		return nil, fmt.Errorf("invalid search results: %q not found", youtubeCommunityPostsStart)
	}

	buf.Reset()
	_, err = buf.Write(content[communityPostsIdx+len(youtubeCommunityPostsStart):])
	if err != nil {
		return nil, fmt.Errorf("truncating search results: %w", err)
	}

	var results []youtubeCommunityPost
	dec := json.NewDecoder(buf)
	err = dec.Decode(&results)
	if err != nil {
		return nil, fmt.Errorf("parsing search results: %w", err)
	}

	posts := make([]*feed.Post, 0, len(results))
	for _, result := range results {
		data := result.BackstagePostThreadRenderer.Post.BackstagePostRenderer
		if data.PostID == "" {
			continue // non backstagePostRenderer
		}

		date, err := parseYoutubeTimeText(data.PublishedTimeText.Runs[0].Text)
		if err != nil {
			return nil, fmt.Errorf("invalid timestamp: %w", err)
		}

		description := ""
		for _, run := range data.ContentText.Runs {
			description += run.Text
		}

		post := &feed.Post{
			ID:              data.PostID,
			DescriptionHTML: description,
			URL:             "https://youtube.com/post/" + url.QueryEscape(data.PostID),
			Date:            *date,
			DateString:      data.PublishedTimeText.Runs[0].Text,
		}

		posts = append(posts, post)
	}

	return posts, nil
}

var youtubeCommunityPostsStart = []byte(`{"itemSectionRenderer":{"contents":`)

// youtubeCommunityPost is the internal JSON format that YouTube uses to
// render community posts on their website.
//
// "itemSectionRenderer": {
//   "contents": [
//     {
//       "backstagePostThreadRenderer": {
//         "post": {
//           "backstagePostRenderer": {
//             "postId": "UgwvfmsbaKk_pYImxll4AaABCQ",
//             "contentText": {
//               "runs": [ { "text": "I had the immense pleasure of filming with " },
//                 ...
//               ]
//             },
//             "backstageAttachment": {
//               "videoRenderer": {
//                 "videoId": "d9zHO6Lh2zY",
//                 "thumbnail": {
//                   "thumbnails": [ { "url": "https://i.ytimg.com/vi/d9zHO6Lh2zY/hq720.jpg?sqp=-oaymwEjCOgCEMoBSFryq4qpAxUIARUAAAAAGAElAADIQj0AgKJDeAE=&rs=AOn4CLBfEhxren7vOP7R9ATaCilq0dE1Pg", "width": 360, "height": 202 },
//                     ...
//                   ]
//                 },
//                 "title": {
//                   "runs": [ { "text": "Tom Scott plus: the new second channel" }
//                   ],
//                 },
//                 "descriptionSnippet": {
//                   "runs": [ { "text": "I don't get to work with my friends all that much. It's time to change that. New videos every other Saturday, starting 4th September. Unscripted, unrehearsed, and out of my comfort zone: this..." } ]
//                 },
//                 "longBylineText": {},
//                 "publishedTimeText": {
//                   "simpleText": "vor 4 Tagen"
//                 },
//                 "viewCountText": {
//                   "simpleText": "228.799 Aufrufe"
//                 }
//               }
//             },
//             "publishedTimeText": {
//               "runs": [ { "text": "vor 3 Tagen", "navigationEndpoint": }
//               ]
//             },
//             "voteCount": {
//               "simpleText": "473"
//             },
type youtubeCommunityPost struct {
	BackstagePostThreadRenderer struct {
		Post struct {
			BackstagePostRenderer struct {
				PostID      string `json:"postId"`
				ContentText struct {
					Runs []struct {
						Text string `json:"text"`
					} `json:"runs"`
				} `json:"contentText"`
				PublishedTimeText struct {
					Runs []struct {
						Text string `json:"text"`
					} `json:"runs"`
				} `json:"publishedTimeText"`
			} `json:"backstagePostRenderer"`
		} `json:"post"`
	} `json:"backstagePostThreadRenderer"`
}

func parseYoutubeTimeText(s string) (*time.Time, error) {
	parts := strings.SplitN(s, " ", 4)
	if len(parts) < 3 {
		return nil, fmt.Errorf("unexpected time format %q (%d parts)", s, len(parts))
	}

	num, err := strconv.ParseUint(parts[0], 10, 0)
	if err != nil {
		return nil, fmt.Errorf("unexpected time format %q (invalid number): %w", s, err)
	}

	if parts[2] != "ago" {
		return nil, fmt.Errorf("unexpected time format %q (\"ago\" not found)", s)
	}

	t := time.Now()
	switch parts[1] {
	case "minute", "minutes":
		t = t.Add(-time.Duration(num) * time.Minute).Truncate(time.Second)
	case "hour", "hours":
		t = t.Add(-time.Duration(num) * time.Hour).Truncate(time.Minute)
	case "day", "days":
		t = t.AddDate(0, 0, -int(num)).Truncate(24 * time.Hour)
	case "week", "weeks":
		t = t.AddDate(0, 0, -int(num)*7).Truncate(24 * time.Hour)
	case "month", "months":
		t = t.AddDate(0, -int(num), 0).Truncate(24 * time.Hour)
	case "year", "years":
		t = t.AddDate(-int(num), 0, 0).Truncate(24 * time.Hour)
	default:
		return nil, fmt.Errorf("unexpected time format %q (can't parse %q)", s, parts[1])
	}

	t = t.UTC()
	return &t, nil
}
