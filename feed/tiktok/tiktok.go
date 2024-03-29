package tiktok

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"

	"github.com/heyLu/numblr/feed"
)

var tiktokRequestCountMu sync.Mutex
var tiktokRequestCount = 0

func init() {
	go resetTiktokRequestCount()
}

func resetTiktokRequestCount() {
	for {
		time.Sleep(1 * time.Minute)
		tiktokRequestCountMu.Lock()
		tiktokRequestCount = 0
		tiktokRequestCountMu.Unlock()
	}
}

func canDoTiktokRequest() error {
	tiktokRequestCountMu.Lock()
	requestCount := tiktokRequestCount
	tiktokRequestCount++
	tiktokRequestCountMu.Unlock()

	if requestCount > 10 {
		return fmt.Errorf("too many tiktok requests, slow down a bit (%d)", requestCount)
	}

	return nil
}

var accountDataMatcher = cascadia.MustCompile("script#SIGI_STATE")
var accountRefRE = regexp.MustCompile(`@(([A-Z]\w+ [A-Z])?\w+)`)
var tagRE = regexp.MustCompile(`#(\w+)`)

type tiktok struct {
	name string

	accountData tiktokAccountData
	postIDs     []string
}

type tiktokAccountData struct {
	SharingMeta struct {
		Value struct {
			Description string `json:"og:description"`
			Image       string `json:"og:image"`
		} `json:"value"`
	} `json:"SharingMeta"`
	ItemList struct {
		UserPost struct {
			List []string `json:"list"`
		} `json:"user-post"`
		Challenge struct {
			List []string `json:"list"`
		} `json:"challenge"`
	} `json:"ItemList"`
	ItemModule map[string]struct {
		ID          string `json:"id"`
		Description string `json:"desc"`
		CreateTime  string `json:"createTime"`
		Video       struct {
			Width         int    `json:"width"`
			Height        int    `json:"height"`
			Cover         string `json:"cover"`
			PlayAddr      string `json:"playAddr"`
			SubtitleInfos []struct {
				LanguageID       string `json:"LanguageID"`
				LanguageCodeName string `json:"LanguageCodeName"`
				URL              string `json:"Url"`
				Format           string `json:"Format"`
				Source           string `json:"Source"`
			} `json:"subtitleInfos"`
		} `json:"video"`
		Author string `json:"author"`
		Music  struct {
			Title      string `json:"title"`
			PlayURL    string `json:"playUrl"`
			AuthorName string `json:"authorName"`
			Album      string `json:"album"`
		} `json:"music"`
		Stats struct {
			DiggCount    int `json:"diggCount"`
			ShareCount   int `json:"shareCount"`
			CommentCount int `json:"commentCount"`
			PlayCount    int `json:"playCount"`
		} `json:"stats"`
	} `json:"ItemModule"`
	UserPage struct {
		UniqueID string `json:"uniqueId"`
	} `json:"UserPage"`
}

// Open fetches the feed for user `name` from TikTok.
func Open(ctx context.Context, name string, _ feed.Search) (feed.Feed, error) {
	nameIdx := strings.Index(name, "@")
	if !strings.Contains(name, "https://") && nameIdx != -1 {
		name = "https://www.tiktok.com/@" + name[:nameIdx]
	}

	err := canDoTiktokRequest()
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", name, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:100.0) Gecko/20100101 Firefox/100.0")
	req.Header.Set("Referer", "https://www.tiktok.com/")

	httpClient := &http.Client{}
	httpClient.Jar, _ = cookiejar.New(nil)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %q: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, feed.StatusError{Code: resp.StatusCode}
	}

	node, err := html.Parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse feed: %w", err)
	}

	accountDataEl := cascadia.QueryAll(node, accountDataMatcher)
	if len(accountDataEl) != 1 {
		buf := new(bytes.Buffer)
		_ = html.Render(buf, node)
		log.Printf("could not find account data in %q", buf.String())
		return nil, fmt.Errorf("could not find account data, %d matches", len(accountDataEl))
	}

	var accountData tiktokAccountData
	err = json.Unmarshal([]byte(accountDataEl[0].FirstChild.Data), &accountData)
	if err != nil {
		return nil, fmt.Errorf("parse account data: %w", err)
	}

	if accountData.UserPage.UniqueID != "" {
		name = accountData.UserPage.UniqueID + "@tiktok"
	}

	postIDs := accountData.ItemList.UserPost.List
	if len(postIDs) == 0 {
		postIDs = accountData.ItemList.Challenge.List
	}
	if len(postIDs) == 0 {
		return nil, fmt.Errorf("no posts found, unsupported page?")
	}

	return &tiktok{
		name: name,

		accountData: accountData,
		postIDs:     postIDs,
	}, nil
}

func (tt *tiktok) Name() string {
	return tt.name
}

func (tt *tiktok) Description() string {
	return tt.accountData.SharingMeta.Value.Description
}

func (tt *tiktok) URL() string {
	return tt.name
}

func (tt *tiktok) Next() (*feed.Post, error) {
	if len(tt.postIDs) == 0 {
		return nil, io.EOF
	}

	id := tt.postIDs[0]
	tt.postIDs = tt.postIDs[1:]

	postData, ok := tt.accountData.ItemModule[id]
	if !ok {
		return nil, fmt.Errorf("missing post details for post %q", id)
	}

	createTime, err := strconv.ParseInt(postData.CreateTime, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("invalid timestamp %q: %w", postData.CreateTime, err)
	}
	date := time.Unix(createTime, 0)

	buf := new(bytes.Buffer)
	fmt.Fprintf(buf, `<video preload="none" controls muted loading="lazy" poster=%q src=%q width="%d" height="%d">`,
		postData.Video.Cover, postData.Video.PlayAddr, postData.Video.Width, postData.Video.Height)
	sort.Slice(postData.Video.SubtitleInfos, func(i, j int) bool {
		return postData.Video.SubtitleInfos[i].LanguageID < postData.Video.SubtitleInfos[j].LanguageID
	})
	for _, subtitle := range postData.Video.SubtitleInfos {
		label := subtitle.LanguageCodeName
		if subtitle.Source == "MT" {
			label += " 🤖"
		} else {
			label += " (" + subtitle.Source + ")"
		}

		// note: proxy is necessary because `track` src must be same-origin (crossorigin does not work because of tiktok's CORS headers)
		if subtitle.LanguageCodeName == "eng-US" {
			fmt.Fprintf(buf, `	<track default kind="captions" srclang="en" label=%q src=%q />`, label, "/proxy?url="+subtitle.URL)
		} else {
			fmt.Fprintf(buf, `	<track kind="captions" label=%q src=%q />`, label, "/proxy?url="+subtitle.URL)
		}
		fmt.Fprintln(buf)

	}
	fmt.Fprintln(buf, `</video>`)

	description := postData.Description
	description = accountRefRE.ReplaceAllStringFunc(description, func(accountRef string) string {
		accountLink := accountRef[1:]
		if strings.Contains(accountLink, " ") {
			accountLink = strings.Replace(strings.ToLower(accountLink), " ", "", -1)
		}
		return fmt.Sprintf("<a href=%q>%s</a>", "/"+accountLink+"@tiktok", accountRef)
	})
	description = tagRE.ReplaceAllStringFunc(description, func(tag string) string {
		return fmt.Sprintf(`<a class="tag" href="/?feeds=%s">%s</a>`, url.QueryEscape("https://www.tiktok.com/tag/"+tag[1:]), tag)
	})

	fmt.Fprintf(buf, `<p>%s</p>`, description)

	if tt.name != postData.Author+"@tiktok" {
		fmt.Fprintf(buf, "<p>Originally by <a href=%q>%s</a>.</p>", postData.Author+"@tiktok", postData.Author)
		fmt.Fprintln(buf)
	}

	if postData.Music.PlayURL != "" {
		fmt.Fprintf(buf, `<p>Music: %s from %s by %s: `, postData.Music.Title, postData.Music.Album, postData.Music.AuthorName)
		fmt.Fprintf(buf, `<br /><audio preload="none" controls loading="lazy" src=%q></audio>`, postData.Music.PlayURL)
		fmt.Fprintf(buf, `</p>`)
	}

	fmt.Fprintf(buf, `<p>%d ❤, %d 📮, %d 💬, %d 🎶`, postData.Stats.DiggCount, postData.Stats.ShareCount, postData.Stats.CommentCount, postData.Stats.PlayCount)
	fmt.Fprintln(buf)

	tags := make([]string, 0, 1)
	for _, maybeTag := range strings.Fields(postData.Description) {
		if len(maybeTag) > 2 && maybeTag[0] == '#' {
			tags = append(tags, maybeTag[1:])
		}
	}

	return &feed.Post{
		Source:          "tiktok",
		ID:              id,
		URL:             "https://www.tiktok.com/@" + postData.Author + "/video/" + id,
		Title:           "",
		Author:          postData.Author + "@tiktok",
		AvatarURL:       tt.accountData.SharingMeta.Value.Image,
		DescriptionHTML: buf.String(),
		Tags:            tags,
		DateString:      date.UTC().String(),
		Date:            date.UTC(),
	}, nil
}

func (tt *tiktok) Close() error {
	return nil
}
