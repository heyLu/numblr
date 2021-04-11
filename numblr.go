package main

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NYTimes/gziphandler"
	"github.com/gorilla/mux"
	"github.com/hashicorp/golang-lru"
	"golang.org/x/net/html"
)

type Post struct {
	Source          string
	ID              string `xml:"guid"`
	Author          string
	AvatarURL       string
	URL             string   `xml:"link"`
	Title           string   `xml:"title"`
	DescriptionHTML string   `xml:"description"`
	Tags            []string `xml:"category"`
	DateString      string   `xml:"pubDate"`
	Date            time.Time
}

var isReblogRE = regexp.MustCompile(`^\s*[-\w]+:`)

func (p Post) IsReblog() bool {
	return isReblogRE.MatchString(p.Title)
}

var contentNoteRE = regexp.MustCompile(`\b(tw|trigger warning|cn|content note|cw|content warning)\b`)
var imgRE = regexp.MustCompile(`<img `)
var widthHeightRE = regexp.MustCompile(` (width|height|style)="[^"]+"`)
var origWidthHeightRE = regexp.MustCompile(`data-orig-width="(\d+)" data-orig-height="(\d+)"`)
var origHeightWidthRE = regexp.MustCompile(`data-orig-height="(\d+)" data-orig-width="(\d+)"`)
var blankLinksRE = regexp.MustCompile(` target="_blank"`)
var linkRE = regexp.MustCompile(`<a `)
var tumblrReblogLinkRE = regexp.MustCompile(`<a ([^>]*)href="(https?://[^.]+\.tumblr.com([^" ]+)?)"([^>]*)>([-\w]+)</a>\s*:`) // <a>account</a>:
var tumblrAccountLinkRE = regexp.MustCompile(`<a ([^>]*)href="[^"]+"([^>]*)>@([-\w]+)</a>`)                                   // @<account>
var tumblrLinksRE = regexp.MustCompile(`https?://([^.]+).t?umblr.com([^" ]+)?`)
var instagramLinksRE = regexp.MustCompile(`https?://(www\.)?instagram.com/([^/" ]+)[^" ]*`)
var videoRE = regexp.MustCompile(`<video `)
var autoplayRE = regexp.MustCompile(` autoplay="autoplay"`)

const CookieName = "numbl"
const UserAgent = "numblr"

var config struct {
	Addr         string
	DatabasePath string

	DefaultFeed string

	AppDisplayMode string

	CollectStats bool
}

const CacheTime = 10 * time.Minute
const AvatarSize = 32
const AvatarCacheTime = 30 * 24 * time.Hour

const GroupPostsNumber = 5
const TagsCollapseCount = 20

//go:embed favicon.png
var FaviconPNGBytes []byte

var cacheFn CacheFn = NewCached

var cache *lru.Cache
var avatarCache *lru.Cache

var httpClient = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

type userAgentTransport struct {
	UserAgent string
	Transport http.RoundTripper
}

func (uat *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", uat.UserAgent)
	return uat.Transport.RoundTrip(req)
}

func main() {
	flag.StringVar(&config.Addr, "addr", "localhost:5555", "Address to listen on")
	flag.StringVar(&config.DatabasePath, "db", "", "Database path to use")
	flag.StringVar(&config.DefaultFeed, "default", "staff,engineering", "Default feeds to view")
	flag.StringVar(&config.AppDisplayMode, "app-display", "browser", "Display mode to use when installed as an app")
	flag.BoolVar(&config.CollectStats, "stats", false, "Whether to collect anonymized stats (num cached feeds & posts, recent errors & user agents")
	flag.StringVar(&NitterURL, "nitter-url", "https://nitter.net", "Nitter instance to use")
	flag.StringVar(&BibliogramInstancesURL, "bibliogram-instances-url", BibliogramInstancesURL, "The bibliogram url to use to fetch possible instances from")
	flag.Parse()

	http.DefaultClient.Timeout = 10 * time.Second
	http.DefaultClient.Transport = &userAgentTransport{
		UserAgent: UserAgent,
		Transport: http.DefaultTransport,
	}

	// TODO: unify in-memory cache and database into a pluggable interface
	if config.DatabasePath != "" {
		db, err := InitDatabase(config.DatabasePath)
		if err != nil {
			log.Fatalf("setup database: %s", err)
		}

		cacheFn = func(ctx context.Context, name string, uncachedFn FeedFn, search Search) (Feed, error) {
			return NewDatabaseCached(ctx, db, name, uncachedFn, search)
		}

		if config.CollectStats {
			EnableDatabaseStats(db, config.DatabasePath)
		}

		go func() {
			maxConcurrentFeeds := make(chan bool, 100)

			refreshFn := func() {
				feeds, err := ListFeedsOlderThan(context.Background(), db, time.Now().Add(-CacheTime))
				if err != nil {
					log.Printf("Error: listing feeds in background: %s", err)
					return
				}

				for _, feedName := range feeds {
					func(feedName string) {
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()

						feed, err := NewCachedFeed(ctx, feedName, cacheFn, Search{})
						if err != nil {
							log.Printf("Error: background refresh: opening %s: %s", feedName, err)
							return
						}
						maxConcurrentFeeds <- true
						defer func() {
							err := feed.Close()
							if err != nil {
								log.Printf("Error: background refresh: closing %s: %s", feedName, err)
							}
							<-maxConcurrentFeeds
						}()

						_, err = feed.Next()
						for err == nil {
							_, err = feed.Next()
						}

						if err != nil && !errors.Is(err, io.EOF) {
							log.Printf("Error: background refresh: iterating %s: %s", feedName, err)
							return
						}
					}(feedName)
				}

				if len(feeds) > 0 {
					log.Printf("Refreshed %d feeds", len(feeds))
				}
			}

			for {
				go refreshFn()

				time.Sleep(1 * time.Minute)
			}
		}()
	}

	var err error
	cache, err = lru.New(100)
	if err != nil {
		log.Fatal("setup cache:", err)
	}
	avatarCache, err = lru.New(100)
	if err != nil {
		log.Fatal("setup avatar cache:", err)
	}

	router := mux.NewRouter()
	router.Use(gziphandler.GzipHandler)
	router.Use(strictTransportSecurity)

	router.Handle("/stats", http.HandlerFunc(StatsHandler))
	if config.CollectStats {
		EnableStats(20, 20)
	}

	router.HandleFunc("/favicon.ico", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/favicon.png", http.StatusPermanentRedirect)
	})
	router.HandleFunc("/favicon.png", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(FaviconPNGBytes)
		return
	})

	router.HandleFunc("/robots.txt", func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprintln(w, `User-agent: *
Disallow: /`)
	})

	router.HandleFunc("/manifest.webmanifest", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/manifest+json")

		fmt.Fprintf(w, `{
  "name": "Numblr",
  "description": "Alternative Tumblr (and Twitter, Instagram, AO3, RSS, ...) frontend.",
  "short_name": "numblr",
  "lang": "en",
  "start_url": "/",
  "icons": [{
    "src": "/favicon.png",
    "sizes": "192x192",
	 "purpose": "any maskable",
	 "type": "image/png"
  }],
  "display": %q,
  "orientation": "portrait",
  "background_color": "#222222",
  "theme_color": "#222222"
}`, config.AppDisplayMode)
	})
	// required to be registered as a progressive web app (?)
	router.HandleFunc("/service-worker.js", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		fmt.Fprint(w, `// required to be registered as a progressive web app
// may cache things later, or provide notifications

self.addEventListener('install', function(e) {
  e.waitUntil(Promise.resolve()).then(() => {
    console.log("numblr installed!");
  });
});
`)
	})

	// TODO: implement a follow button (first only for generic feed, either add to cookie or url, depending on context)

	router.HandleFunc("/settings", func(w http.ResponseWriter, req *http.Request) {
		list := req.FormValue("list")
		feeds := req.FormValue("feeds")

		first := true
		cookieValue := ""
		for _, feed := range strings.Split(feeds, "\n") {
			feed = strings.TrimSpace(feed)
			if feed == "" {
				continue
			}
			if !first {
				cookieValue += ","
			}
			first = false
			cookieValue += feed
		}

		redirect := "/"
		cookieName := CookieName
		if list != "" {
			redirect = "/list/" + list
			cookieName = CookieName + "-list-" + list
		}

		if cookieValue == "" {
			http.Redirect(w, req, redirect, http.StatusTemporaryRedirect)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     cookieName,
			Value:    cookieValue,
			MaxAge:   365 * 24 * 60 * 60, // one year
			SameSite: http.SameSiteLaxMode,
			HttpOnly: true,
		})
		http.Redirect(w, req, redirect, http.StatusSeeOther)
	}).Methods("POST")

	router.HandleFunc("/settings/clear", func(w http.ResponseWriter, req *http.Request) {
		cookie, err := req.Cookie(CookieName)
		if err != nil {
			http.Redirect(w, req, "/", http.StatusTemporaryRedirect)
			return
		}

		cookie.Value = ""
		cookie.MaxAge = -1
		http.SetCookie(w, cookie)
		http.Redirect(w, req, "/", http.StatusSeeOther)
	}).Methods("POST")

	router.HandleFunc("/", HandleTumblr)
	router.HandleFunc("/{feeds}", HandleTumblr)

	router.HandleFunc("/list/{list}", HandleTumblr)

	router.HandleFunc("/{tumblr}/post/{postId}", HandlePost)
	router.HandleFunc("/{tumblr}/post/{postId}/{slug}", HandlePost)

	router.HandleFunc("/avatar/{tumblr}", HandleAvatar)

	http.Handle("/", router)

	log.Printf("Listening on http://%s", config.Addr)
	log.Fatal(http.ListenAndServe(config.Addr, nil))
}

func HandleAvatar(w http.ResponseWriter, req *http.Request) {
	tumblr := mux.Vars(req)["tumblr"]

	avatar, isCached := avatarCache.Get(tumblr)
	if isCached {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d, immutable", int(AvatarCacheTime.Seconds())))
		w.Write(avatar.([]byte))
		return
	}

	var avatarURL string
	switch {
	case strings.Contains(tumblr, "@"):
		http.Error(w, fmt.Sprintf("Error: fetching avatar for %q not supported", tumblr), http.StatusInternalServerError)
		return
	case strings.Contains(tumblr, "."):
		avatarURL = "http://" + tumblr + "/favicon.ico"
	default:
		avatarURL = fmt.Sprintf("https://api.tumblr.com/v2/blog/%s.tumblr.com/avatar/%d", url.PathEscape(tumblr), AvatarSize)
	}

	req, err := http.NewRequestWithContext(req.Context(), "GET", avatarURL, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error: fetching avatar: could not create request: %s", err), http.StatusInternalServerError)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error: fetching avatar: %s", err), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		http.Error(w, fmt.Sprintf("Error: fetching avatar: unexpected status code %d", resp.StatusCode), http.StatusInternalServerError)
		return
	}

	buf := new(bytes.Buffer)
	wr := io.MultiWriter(w, buf)

	avatar = resp.Header.Get("Location")
	w.Header().Set("Cache-Control", fmt.Sprintf("max-age=%d", int(AvatarCacheTime.Seconds())))

	_, err = io.Copy(wr, resp.Body)
	if err != nil {
		log.Printf("could not write avatar: %s", err)
		return
	}

	avatarCache.Add(tumblr, buf.Bytes())
}

func strictTransportSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Strict-Transport-Security", fmt.Sprintf("max-age=%d", 365*24*60*60))
		next.ServeHTTP(w, req)
	})
}

func HandleTumblr(w http.ResponseWriter, req *http.Request) {
	start := time.Now()

	go CountView()
	go CollectUser(req.Header.Get("User-Agent"))

	if req.URL.Query().Get("feed") != "" {
		feed := req.URL.Query().Get("feed")
		if strings.ContainsAny(feed, "#?") {
			feed = url.PathEscape(feed)
		}
		http.Redirect(w, req, "/"+feed, http.StatusFound)
		return
	}

	settings := SettingsFromRequest(req)
	search := parseSearch(req)

	var feed Feed
	var err error
	feeds := make([]Feed, len(settings.SelectedFeeds))
	var wg sync.WaitGroup
	wg.Add(len(settings.SelectedFeeds))
	for i := range settings.SelectedFeeds {
		go func(i int) {
			defer wg.Done()

			if strings.HasPrefix(settings.SelectedFeeds[i], ":") {
				return
			}

			var openErr error
			feeds[i], openErr = NewCachedFeed(req.Context(), settings.SelectedFeeds[i], cacheFn, search)
			if openErr != nil {
				err = fmt.Errorf("%s: %w", settings.SelectedFeeds[i], openErr)
			}
		}(i)
	}

	limit := 25
	limitParam := req.URL.Query().Get("limit")
	if limitParam != "" {
		l, err := strconv.Atoi(limitParam)
		if err != nil {
			log.Printf("Error: parsing limit: %s", err)
		} else {
			limit = l
		}
	}

	w.Header().Set("Content-Type", `text/html; charset="utf-8"`)

	nightModeCSS := `body { color: #fff; background-color: #222; }.tags a,.tags a:visited{ color: #b7b7b7; text-decoration: none;}a { color: pink; }a:visited { color: #a67070; }article,details:not([open]){ border-bottom: 1px solid #666; }blockquote:not(:last-child) { border-bottom: 1px solid #333; }a.author,a.author:visited,a.tumblr-link,a.tumblr-link:visited{color: #fff;}img{filter: brightness(.8) contrast(1.2);}`
	modeCSS := `@media (prefers-color-scheme: dark) {` + nightModeCSS + `}`
	if _, ok := req.URL.Query()["night-mode"]; ok {
		modeCSS = nightModeCSS
	}
	title := strings.Join(settings.SelectedFeeds, ",")
	if req.URL.Path == "" || req.URL.Path == "/" {
		title = "everything"
	} else if mux.Vars(req)["list"] != "" {
		title = mux.Vars(req)["list"]
	}
	fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
	<meta charset="utf-8" />
	<meta name="viewport" content="width=device-width,minimum-scale=1,initial-scale=1" />
	<meta name="color-scheme" content="dark light" />
	<meta name="description" content="Mirror of %s feeds" />
	<title>%s</title>
	<style>.jumper { font-size: 2em; float: right; text-decoration: none; }.jump-to-top { position: sticky; bottom: 0.25em; }blockquote, figure { margin: 0; }blockquote:not(:last-child) { border-bottom: 1px solid #ddd; } blockquote.question{padding-left: 2em;}blockquote.question ::before, blockquote.question ::after { content: "“"; font-family: serif; font-size: x-large; }body { scroll-behavior: smooth; font-family: sans-serif; overflow-wrap: break-word; }article,details:not([open]){ border-bottom: 1px solid black; padding-bottom: 1em; margin-bottom: 1em; }article h1 a, article h4 a { text-decoration: none; border-bottom: 1px dotted black; }.tags { list-style: none; padding: 0; color: #666; }.tags li, .tags display, tags display[open] { display: inline }.tags a, .tags a:visited{color: #333; text-decoration: none;}img:not(.avatar), video, iframe { max-width: 100%%; height: auto; object-fit: contain }@media (min-width: 60em) { body { margin: 0 auto; max-width: 60em; } img:not(.avatar), video { max-height: 50vh; width: auto; object-fit: contain; } img:hover:not(.avatar), video:hover { max-height: 100%%; width: auto; object-fit: contain; }}.avatar{width: 1em;height: 1em;vertical-align: middle;display:inline-block;}a.author,a.author:visited,a.tumblr-link,a.tumblr-link:visited{color: #000; font-weight: bold;}a.tumblr-link{padding: 0.5em; text-decoration: none; font-size: larger; vertical-align: middle;}.next-page { display: flex; justify-content: center; padding: 1em; }.ao3 dl dt, .ao3 dl dd { display: inline; margin-left: 0}.ao3 blockquote { border: none; }textarea{ width: 100%%; }%s</style>
	<link rel="preconnect" href="https://64.media.tumblr.com/" />
	<link rel="manifest" href="/manifest.webmanifest" />
	<meta name="theme-color" content="#222222" />
	<link rel="icon" href="/favicon.png" />
</head>

<body>

<a class="jumper" href="#bottom">▾</a>

<h1>%s</h1>

`, title, title, modeCSS, title)

	fmt.Fprintf(w, `<form method="GET" action=%q><input aria-label="visit feed" name="feed" type="search" value="" placeholder="feed" list="feeds" /></form>`, req.URL.Path)
	fmt.Fprintln(w, `<datalist id="feeds">`)
	for _, tumbl := range settings.SelectedFeeds {
		fmt.Fprintf(w, `<option value=%q>%s</option>`, tumbl, tumbl)
	}
	fmt.Fprintln(w, `</datalist>`)
	fmt.Fprintf(w, `<form method="GET" action=%q><input aria-label="search posts" name="search" type="search" value=%q placeholder="noreblog #art ..." /></form>`, req.URL.Path, req.URL.Query().Get("search"))

	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	wg.Wait()
	successfulFeeds := make([]Feed, 0, len(feeds))
	for _, feed := range feeds {
		if feed == nil {
			continue
		}
		successfulFeeds = append(successfulFeeds, feed)
	}
	feed = MergeFeeds(successfulFeeds...)
	if err != nil {
		go CollectError(err)
		log.Println("open:", err)
		fmt.Fprintf(w, `<code style="color: red; font-weight: bold; font-size: larger;">could not load feed: %s</code>`, err)
		if feed == nil {
			return
		}
	}
	defer func() {
		err := feed.Close()
		if err != nil {
			log.Printf("Error: closing %s: %s", settings.SelectedFeeds, err)
		}
	}()
	openTime := time.Since(start)

	postCount := 0
	var post *Post
	var lastPost *Post
	nextPost := func() {
		lastPost = post
		post, err = feed.Next()
	}

	if search.BeforeID != "" {
		nextPost()
		for err == nil {
			if post.ID <= search.BeforeID {
				break
			}
			nextPost()
		}
	}

	posts := make([]*Post, 0, limit)

	nextPost()
	for err == nil {
		if !search.Matches(post) {
			nextPost()
			continue
		}

		if postCount >= limit {
			break
		}

		postCount++

		posts = append(posts, post)

		nextPost()
	}

	postGroups := make([][]*Post, 0, limit)

	group, rest := nextPostsGroup(posts, GroupPostsNumber)
	for len(rest) != 0 {
		postGroups = append(postGroups, group)

		group, rest = nextPostsGroup(rest, GroupPostsNumber)
	}
	if len(group) > 0 {
		postGroups = append(postGroups, group)
	}

	imageCount := 0
	for _, group := range postGroups {
		if len(settings.SelectedFeeds) > 1 && len(group) >= GroupPostsNumber {
			fmt.Fprintf(w, `<details open><summary>%d posts by %s</summary>`, len(group), group[0].Author)
		}

		for _, post := range group {
			classes := make([]string, 0, 1)
			if post.IsReblog() {
				classes = append(classes, "reblog")
			}

			classes = append(classes, post.Source)

			fmt.Fprintf(w, `<article class=%q>`, strings.Join(classes, " "))
			avatarURL := post.AvatarURL
			if avatarURL == "" {
				avatarURL = "/avatar/" + post.Author
			}
			fmt.Fprintf(w, `<p><img class="avatar" src="%s" loading="lazy" /> <a class="author" href="/%s">%s</a>:<p>`, avatarURL, post.Author, post.Author)

			if len(post.Tags) > 0 {
				fmt.Fprint(w, `<ul class="tags content-notes">`)
				for _, tag := range post.Tags {
					if contentNoteRE.MatchString(tag) {
						fmt.Fprintf(w, `<li>#%s</li> `, tag)
					}
				}
				fmt.Fprintln(w, `</ul>`)
			}

			fmt.Fprintln(w, `<section class="post-content">`)

			postHTML := ""
			if post.Title != "Photo" && !post.IsReblog() {
				postHTML = html.UnescapeString(post.Title)
			}
			if post.Source == "tumblr" && post.IsReblog() {
				reblogHTML, err := FlattenReblogs(post.DescriptionHTML)
				if err != nil {
					log.Printf("Error: flatten reblog: %s", err)
				}
				postHTML = reblogHTML
			} else {
				postHTML += post.DescriptionHTML
			}
			postHTML = strings.ReplaceAll(postHTML, "<body>", "")
			postHTML = strings.ReplaceAll(postHTML, "</body>", "")
			// load first 5 images eagerly, and the rest lazily
			postHTML = imgRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
				imageCount++
				if imageCount > 0 {
					return `<img loading="lazy" `
				}
				return `<img `
			})
			//postHTML = widthHeightRE.ReplaceAllString(postHTML, ` `)
			postHTML = origWidthHeightRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
				parts := origWidthHeightRE.FindStringSubmatch(repl)
				if len(parts) != 3 {
					log.Printf("Error: invalid orig-width-height: %s", repl)
					return repl
				}

				return fmt.Sprintf(`width=%q height=%q`, parts[1], parts[2])
			})
			postHTML = origHeightWidthRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
				parts := origHeightWidthRE.FindStringSubmatch(repl)
				if len(parts) != 3 {
					log.Printf("Error: invalid orig-width-height: %s", repl)
					return repl
				}

				return fmt.Sprintf(`width=%q height=%q`, parts[2], parts[1])
			})
			postHTML = blankLinksRE.ReplaceAllString(postHTML, ` `)
			postHTML = linkRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
				return `<a rel="noreferrer" `
			})
			postHTML = tumblrReblogLinkRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
				parts := tumblrReblogLinkRE.FindStringSubmatch(repl)
				if len(parts) != 6 {
					log.Printf("Error: invalid tumblr reblog link: %s", repl)
					return repl
				}

				u, err := url.Parse(parts[2])
				if err != nil {
					log.Printf("could not parse url: %s", err)
					return repl
				}

				tumblrName := u.Host[:strings.Index(u.Host, ".")]
				u.Host = ""
				u.Scheme = ""
				u.Path = path.Join("/", tumblrName, u.Path)

				reblogLink := u.String()
				tumblrLink := "/" + tumblrName

				return fmt.Sprintf(`<img class="avatar" src=%q loading="lazy" /> <a href=%q>%s</a> (<a %shref=%q%s>post</a>):`, "/avatar/"+tumblrName, tumblrLink, tumblrName, parts[1], reblogLink, parts[4])
			})
			postHTML = tumblrAccountLinkRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
				parts := tumblrAccountLinkRE.FindStringSubmatch(repl)
				if len(parts) != 4 {
					log.Printf("Error: invalid tumblr account link: %s", repl)
					return repl
				}

				return fmt.Sprintf(`<a %shref=%q%s>%s</a>`, parts[1], "/"+parts[3], parts[2], "@"+parts[3])
			})
			postHTML = tumblrLinksRE.ReplaceAllStringFunc(postHTML, tumblrToInternal)
			postHTML = strings.Replace(postHTML, "https://href.li/?", "", -1)
			postHTML = instagramLinksRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
				parts := instagramLinksRE.FindStringSubmatch(repl)
				if len(parts) != 3 {
					log.Printf("Error: invalid instagram link: %s", repl)
					return repl
				}
				return "/" + parts[2] + "@instagram"
			})
			postHTML = videoRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
				return `<video preload="metadata" controls="" `
			})
			postHTML = autoplayRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
				return ``
			})

			fmt.Fprint(w, postHTML)

			fmt.Fprintln(w, `</section>`)

			fmt.Fprint(w, "<footer>")
			if len(post.Tags) > 0 {
				fmt.Fprint(w, `<ul class="tags">`)
				for i, tag := range post.Tags {
					if i == TagsCollapseCount {
						fmt.Fprintf(w, `<details><summary>...</summary> `)
					}

					tagLink := req.URL
					tagParams := tagLink.Query()
					tagParams.Set("search", "#"+tag)
					tagLink.RawQuery = tagParams.Encode()
					fmt.Fprintf(w, `<li><a href=%q>#%s</a></li> `, tagLink, tag)
				}
				if len(post.Tags) >= TagsCollapseCount {
					fmt.Fprintf(w, `</details>`)
				}
				fmt.Fprintln(w, `</ul>`)
			}
			fmt.Fprintf(w, `<time title="%s" datetime="%s">%s ago</time> `, post.Date, post.DateString, prettyDuration(time.Since(post.Date)))
			fmt.Fprintf(w, `by <a href=%q>%s</a>, `, "/"+post.Author, post.Author)
			if post.Source == "tumblr" {
				fmt.Fprintf(w, `<a href=%q title="link to just this post">post</a> <a class="tumblr-link" href=%q>t</a>`, tumblrToInternal(post.URL), post.URL)
			} else {
				fmt.Fprintf(w, `<a href=%q title="link to just this post">post</a>`, post.URL)
			}
			fmt.Fprint(w, "</footer>")
			fmt.Fprintln(w, "</article>")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}

		if len(settings.SelectedFeeds) > 1 && len(group) >= GroupPostsNumber {
			fmt.Fprintln(w, `</details>`)
		}
	}

	fmt.Fprintln(w, `<span id="bottom"></span>
<a id="link-top" class="jumper" href="#">▴</a>`)

	if err == nil && lastPost != nil {
		nextPage := req.URL
		query := url.Values{}
		query.Set("before", lastPost.ID)
		nextPage.RawQuery = query.Encode()
		fmt.Fprintf(w, `<div class="next-page"><a href="%s">next page</a></div>`, nextPage)
	}

	fmt.Fprintf(w, `<form method="POST" action="/settings">

	<input type="text" name="list" hidden value=%q />

	<label for="feeds">Feeds to view by default</label>:
	<div class="field">
		<textarea rows="%d" cols="30" name="feeds">%s</textarea>
	</div>
	<input type="submit" value="Save" />
</form>

<form method="POST" action="/settings/clear">
	<input type="submit" value="Clear" title="FIXME: clear currently broken :/" disabled />
</form>
`, mux.Vars(req)["list"], len(settings.SelectedFeeds)+1, strings.Join(settings.SelectedFeeds, "\n"))

	u := url.URL{
		Path: strings.Join(settings.SelectedFeeds, ","),
	}
	if strings.ContainsAny(u.Path, "/&?") {
		u.Path = "/"
		query := make(url.Values)
		query["feeds"] = settings.SelectedFeeds
		u.RawQuery = query.Encode()
	}
	fmt.Fprintf(w, `<p>Share feed via <a href=%q>a link</a>.</p>`, u.String())

	fmt.Fprintln(w, `<section id="lists">
<h1>Lists</h1>

<ul>

<li><a href="/">everything</a></li>`)

	for _, cookie := range req.Cookies() {
		if strings.HasPrefix(cookie.Name, CookieName+"-list-") {
			listName := cookie.Name[len(CookieName+"-list-"):]
			fmt.Fprintf(w, `<li><a href="/list/%s">%s</a></li>`, listName, listName)
		}
	}
	fmt.Fprintln(w, `</ul>
</section>`)

	fmt.Fprintf(w, `<hr /><footer>%d posts from %q (<a href=%q>source</a>) in %s (open: %s)</footer>`, postCount, feed.Name(), feed.URL(), time.Since(start).Round(time.Millisecond), openTime.Round(time.Millisecond))
	if err != nil && !errors.Is(err, io.EOF) {
		log.Println("decode:", err)
	}

	fmt.Fprintln(w, `<script>
  // pretty reloads (sparkly indicator)

  let lastScrollTop = window.pageYOffset || document.documentElement.scrollTop;;

  var toTopEl = document.querySelector("#link-top");
  window.addEventListener("scroll", function() {
    let st = window.pageYOffset || document.documentElement.scrollTop;
    if (st > lastScrollTop){
      toTopEl.classList.remove("jump-to-top");
    } else {
      toTopEl.classList.add("jump-to-top");
    }
    lastScrollTop = st <= 0 ? 0 : st;
  }, false);

  function reloadSpinner() {
    let reloadStyleEl = document.createElement("style");
    reloadStyleEl.textContent = "#reload { position: fixed; top: 1ex; left: 50vw; animation: reload 3s infinite; } @keyframes reload { 0% { color: black; } 12.5% { color: violet; } 25% { color: blue; } 37.5% { color: green; } 50% { color: yellow; } 62.5% { color: orange; } 75% { color: red; } 87.5% { color: brown; } 100% { color: black; } }";
    document.body.appendChild(reloadStyleEl);
    let reloadEl = document.createElement("div");
    reloadEl.id = "reload";
    reloadEl.textContent = "✦";
    document.body.appendChild(reloadEl);
  };

  window.addEventListener("click", (ev) => {
    if (ev.target.tagName != "A" || new URL(ev.target.href).pathname == window.location.pathname) {
      return;
	 }
	 reloadSpinner();
  });
  window.addEventListener("focus", (ev) => {
    let reloadEl = document.querySelector("#reload");
    if (reloadEl) {
      document.body.removeChild(reloadEl);
	 }
  });

  // pull to reload

  let startY;

  window.addEventListener('touchstart', e => {
    startY = e.touches[0].pageY;
  }, {passive: true});

  window.addEventListener('touchmove', e => {
    const y = e.touches[0].pageY;
    if (document.scrollingElement.scrollTop === 0 && y > startY) {
      let url = new URL(window.location);
      if (url.searchParams.has("before")) {
        url.searchParams.delete("before");
      }
      url.hash = "";
      window.location = url.href;

      reloadSpinner();
    }
  }, {passive: true});

  // skip posts with a double-tap

  let lastTouch = 0;
  window.addEventListener('mousedown', (ev) => {
    let el = ev.target.closest("article");
    if (ev.timeStamp - lastTouch < 500 && el != null) {
      ev.preventDefault();
      //window.scrollBy({top: el.clientHeight, behaviour: 'smooth'});
      window.scrollTo({top: el.offsetTop + el.clientHeight - (window.innerHeight * 0.1), behavior: 'auto'});
    };
    lastTouch = ev.timeStamp;
  });

  // service worker to be detected as a progressive web app in webkit-based browsers

  if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('/service-worker.js')
      .then(() => console.log("numblr registered!"))
		.catch((err) => console.log("numblr registration failed: ", err));
  }
</script>`)

	fmt.Fprintln(w, `</body>
</html>`)
}

func nextPostsGroup(posts []*Post, groupPostsNumber int) (group []*Post, rest []*Post) {
	if len(posts) == 0 || len(posts) == 1 {
		return posts, nil
	}

	i := 0
	for ; i+1 < len(posts); i++ {
		if posts[i].Author != posts[i+1].Author {
			break
		}
	}

	if i+1 >= groupPostsNumber {
		return posts[:i+1], posts[i+1:]
	}

	return []*Post{posts[0]}, posts[1:]
}

func tumblrToInternal(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		log.Printf("could not parse url: %s", err)
		return link
	}

	if u.Path == "/redirect" {
		redirect := u.Query().Get("z")
		if redirect == "" {
			log.Printf("invalid redirect: %q", link)
			return link
		}

		return redirect
	}

	tumblrName := u.Host[:strings.Index(u.Host, ".")]
	u.Host = ""
	u.Scheme = ""
	u.Path = path.Join("/", tumblrName, u.Path)
	return u.String()
}

type Search struct {
	IsActive bool

	BeforeID string

	NoReblogs    bool
	Terms        []string
	Tags         []string
	ExcludeTerms []string
	ExcludeTags  []string
}

func (s *Search) Matches(p *Post) bool {
	if !s.IsActive {
		return true
	}

	if s.NoReblogs && p.IsReblog() {
		return false
	}

	for _, tag := range p.Tags {
		for _, exclude := range s.ExcludeTags {
			if tag == exclude {
				return false
			}
		}
	}

	// must match all tags
	for _, tag := range s.Tags {
		if !contains(p.Tags, tag) {
			return false
		}
	}

	for _, term := range s.Terms {
		if !strings.Contains(strings.ToLower(p.Title), term) && !strings.Contains(strings.ToLower(p.DescriptionHTML), term) {
			return false
		}
	}

	for _, term := range s.ExcludeTerms {
		if strings.Contains(strings.ToLower(p.Title), term) || strings.Contains(strings.ToLower(p.DescriptionHTML), term) {
			return false
		}
	}

	return true
}

func contains(xs []string, contain string) bool {
	for _, x := range xs {
		if strings.ToLower(x) == contain {
			return true
		}
	}
	return false
}

func parseSearch(req *http.Request) Search {
	beforeParam := req.URL.Query().Get("before")

	searchTerms := strings.Fields(req.URL.Query().Get("search"))
	if beforeParam == "" && len(searchTerms) == 0 {
		return Search{}
	}

	search := Search{
		IsActive:     true,
		BeforeID:     beforeParam,
		Terms:        make([]string, 0, 1),
		Tags:         make([]string, 0, 1),
		ExcludeTags:  make([]string, 0, 1),
		ExcludeTerms: make([]string, 0, 1),
	}

	// TODO: allow tags with spaces (#This is fun #Other tag)
	for _, searchTerm := range searchTerms {
		if searchTerm == "noreblog" {
			search.NoReblogs = true
			continue
		}

		exclude := false
		if searchTerm[0] == '-' {
			exclude = true
			searchTerm = searchTerm[1:]
		}

		tag := false
		if searchTerm[0] == '#' {
			tag = true
			searchTerm = searchTerm[1:]
		}

		unescaped, err := url.QueryUnescape(searchTerm)
		if err == nil {
			searchTerm = unescaped
		}

		searchTerm = strings.ToLower(searchTerm)

		switch {
		case exclude && tag:
			search.ExcludeTags = append(search.ExcludeTags, searchTerm)
		case tag:
			search.Tags = append(search.Tags, searchTerm)
		case exclude:
			search.ExcludeTerms = append(search.ExcludeTerms, searchTerm)
		default:
			search.Terms = append(search.Terms, searchTerm)
		}
	}

	return search
}

type Settings struct {
	// SelectedFeeds are the feeds that are explicitely selected, e.g. on
	// the index page, by specifying feeds in the url, or by being on a
	// list page.
	SelectedFeeds []string
}

func SettingsFromRequest(req *http.Request) Settings {
	settings := Settings{}

	isList := strings.HasPrefix(req.URL.Path, "/list/")

	if req.URL.Query()["feeds"] != nil && len(req.URL.Query()["feeds"]) > 0 {
		settings.SelectedFeeds = req.URL.Query()["feeds"]
		return settings
	}

	// explicitely specified in url
	feeds := req.URL.Path[1:]
	if feeds != "" && !isList {
		settings.SelectedFeeds = strings.Split(feeds, ",")
		return settings
	}

	cookieName := CookieName

	if isList {
		cookieName = CookieName + "-list-" + mux.Vars(req)["list"]
	}

	cookie, err := req.Cookie(cookieName)
	if err != nil {
		if err != http.ErrNoCookie {
			log.Printf("getting cookie: %s", err)
		}
		settings.SelectedFeeds = strings.Split(config.DefaultFeed, ",")
		return settings
	}

	if cookie.Value != "" {
		settings.SelectedFeeds = strings.Split(cookie.Value, ",")
		return settings
	}

	settings.SelectedFeeds = strings.Split(config.DefaultFeed, ",")
	return settings
}

func prettyDuration(dur time.Duration) string {
	switch {
	case dur < 24*time.Hour:
		return dur.Round(time.Minute).String()
	case dur < 30*24*time.Hour:
		days := (int(dur.Hours()) / 24)
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	case dur < 365*24*time.Hour:
		months := (int(dur.Hours()) / 24 / 30)
		if months == 1 {
			return "1 month"
		}
		return fmt.Sprintf("%d months", months)
	default:
		years := (int(dur.Hours()) / 24 / 365)
		if years == 1 {
			return "1 year"
		}
		return fmt.Sprintf("%d years", years)
	}
}

func HandlePost(w http.ResponseWriter, req *http.Request) {
	tumblr := mux.Vars(req)["tumblr"]
	postId := mux.Vars(req)["postId"]
	slug := mux.Vars(req)["slug"]
	if slug != "" {
		slug = "/" + slug
	}
	req, err := http.NewRequestWithContext(req.Context(), "GET", fmt.Sprintf("https://%s.tumblr.com/post/%s%s", tumblr, postId, slug), nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error: could not create request: %s", err), http.StatusInternalServerError)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error: could not fetch post: %s", err), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	nightModeCSS := `* { color: #fff !important; background-color: #222 !important; }.tags { color: #b7b7b7 }a { color: pink; }a:visited { color: #a67070; }article{ border-bottom: 1px solid #666; }blockquote:not(:last-child) { border-bottom: 1px solid #333; }a.author,a.author:visited{color: #fff;}`
	modeCSS := `@media (prefers-color-scheme: dark) {` + nightModeCSS + `}`
	if _, ok := req.URL.Query()["night-mode"]; ok {
		modeCSS = nightModeCSS
	}
	fmt.Fprintf(w, `<!doctype html>
<html>
<head>
	<meta charset="utf-8" />
	<meta name="viewport" content="width=device-width,minimum-scale=1,initial-scale=1" />

	<link rel="icon" href=%q />

	<style>h1 { word-break: break-all; }blockquote, figure { margin: 0; }blockquote:not(:last-child) { border-bottom: 1px solid #ddd; } blockquote > blockquote:nth-child(1) { border-bottom: 0; }body { font-family: sans-serif; }article{ border-bottom: 1px solid black; padding: 1em 0; }.tags { list-style: none; padding: 0; color: #666; }.tags > li { display: inline }img, video, iframe { max-width: 95vw; }@media (min-width: 60em) { body { margin: 0 auto; max-width: 60em; } article { max-width: 60em; } img, video { max-height: 50vh; width: auto; } img:hover, video:hover { max-height: 100%%; }}.avatar{height: 1em;}a.author,a.author:visited{color: #000;}%s</style>
	<style>.post-reblog-header img { height: 1em; vertical-align: middle; }.post-reblog-header .post-avatar { display: inline-block; }.post-reblog-header .post-tumblelog-name:after { content: ":"; }</style>
</head>

<body>
`, "/avatar/"+tumblr, modeCSS)

	node, err := html.Parse(resp.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error: could not parse post: %s", err), http.StatusInternalServerError)
		return
	}

	var cleanup func(*html.Node)
	cleanup = func(node *html.Node) {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode {
				switch child.Data {
				case "section":
					if hasAttribute(child, "class", "related-posts-wrapper") {
						node.RemoveChild(child)
						continue
					}
					if hasAttribute(child, "class", "post-controls") {
						node.RemoveChild(child)
						continue
					}
				case "iframe":
					for _, attr := range child.Attr {
						if attr.Key == "src" && strings.Contains(attr.Val, "/photoset_iframe/") {
							photosetImages, err := fetchPhotoset(req.Context(), tumblr, attr.Val)
							if err != nil {
								log.Printf("Error: Invalid photoset %q: %s", attr.Val, err)
								break
							}

							for _, img := range photosetImages {
								node.InsertBefore(img, child)
							}

							break
						}
						if attr.Key == "src" && strings.Contains(attr.Val, "/audio_player_iframe/") {
							u, err := url.Parse(attr.Val)
							if err != nil {
								log.Printf("Error: Invalid audio player %q: %s", attr.Val, err)
								break
							}
							audioURL := u.Query().Get("audio_file")
							audio := html.Node{
								Type: html.ElementNode,
								Data: "audio",
								Attr: []html.Attribute{
									{Key: "src", Val: audioURL},
									{Key: "controls"},
								},
							}
							node.InsertBefore(&audio, child)
							break
						}
					}

					node.RemoveChild(child)
					continue
				case "script", "style":
					node.RemoveChild(child)
					continue
				case "figure":
					attrs := make([]html.Attribute, 0, 2)
					for _, attr := range child.Attr {
						switch attr.Key {
						case "data-orig-width", "data-orig-height":
							attrs = append(attrs, attr)
						}
					}
					child.Attr = attrs
				case "video":
					attrs := []html.Attribute{{Key: "preload", Val: "metadata"}, {Key: "controls"}}
					for _, attr := range child.Attr {
						switch attr.Key {
						case "poster", "muted":
							attrs = append(attrs, attr)
						}
					}
					if child.Parent.Type == html.ElementNode && child.Parent.Data == "figure" {
						for _, attr := range child.Parent.Attr {
							switch attr.Key {
							case "data-orig-width":
								attr.Key = "width"
								attrs = append(attrs, attr)
							case "data-orig-height":
								attr.Key = "height"
								attrs = append(attrs, attr)
							}
						}
					}
					child.Attr = attrs
				default:
					attrs := make([]html.Attribute, 0, len(child.Attr))
					for _, attr := range child.Attr {
						switch attr.Key {
						case "href":
							if strings.Contains(attr.Val, ".tumblr.com") {
								attr.Val = tumblrToInternal(attr.Val)
							} else if attr.Val == "/" {
								attr.Val = "/" + tumblr
							} else if strings.HasPrefix(attr.Val, "/") {
								attr.Val = "/" + tumblr + attr.Val
							}
							attrs = append(attrs, attr)
						case "src", "rel", "title", "alt", "class":
							attrs = append(attrs, attr)
						}
					}
					child.Attr = attrs
				}
			}

			cleanup(child)
		}
	}

	var f func(*html.Node)
	f = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch node.Data {
			case "a", "p", "img", "div", "span":
				if hasAttribute(node, "class", "app-nag") {
					return
				}

				cleanup(node)

				err := html.Render(w, node)
				if err != nil {
					log.Printf("Error: rendering %q: %s", req.URL, err)
				}

				return
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(node)
}

func fetchPhotoset(ctx context.Context, tumblr string, photosetPath string) ([]*html.Node, error) {
	u, err := url.Parse(photosetPath)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	u.Scheme = "https"
	u.Host = tumblr + ".tumblr.com"
	u.Path = strings.Replace(u.Path, "/0/", "/512/", 1)

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	node, err := html.Parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}

	nodes := make([]*html.Node, 0, 10)

	var f func(*html.Node)
	f = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch node.Data {
			case "img":
				nodes = append(nodes, node)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode && child.Data == "img" {
				filterAttributes(child, "src")
				node.RemoveChild(child)
				nodes = append(nodes, child)
				nodes = append(nodes, &html.Node{Type: html.ElementNode, Data: "br"})
				continue
			}

			f(child)
		}
	}
	f(node)

	return nodes, nil
}

func hasAttribute(node *html.Node, attrName, attrValue string) bool {
	for _, attr := range node.Attr {
		if attr.Key == attrName && attr.Val == attrValue {
			return true
		}
	}
	return false
}

func filterAttributes(node *html.Node, keepAttrs ...string) {
	attrs := make([]html.Attribute, 0, len(node.Attr))
	for _, attr := range node.Attr {
		for _, keep := range keepAttrs {
			if attr.Key == keep {
				attrs = append(attrs, attr)
			}
			break
		}
	}
	node.Attr = attrs
}

func MergeFeeds(feeds ...Feed) Feed {
	return &tumblrMerger{feeds: feeds, posts: make([]*Post, len(feeds)), errors: make([]error, len(feeds))}
}

type tumblrMerger struct {
	feeds  []Feed
	posts  []*Post
	errors []error
}

func (tm *tumblrMerger) Name() string {
	name := ""
	first := true
	for _, t := range tm.feeds {
		if !first {
			name += " "
		}
		first = false
		name += t.Name()
	}
	return name
}

func (tm *tumblrMerger) URL() string {
	return ""
}

func (tm *tumblrMerger) Next() (*Post, error) {
	allErrors := false
	for _, err := range tm.errors {
		allErrors = allErrors && err != nil
	}
	if allErrors {
		return nil, tm.errors[0]
	}

	var wg sync.WaitGroup
	wg.Add(len(tm.feeds))
	for i := range tm.feeds {
		go func(i int) {
			if tm.posts[i] == nil && !errors.Is(tm.errors[i], io.EOF) {
				tm.posts[i], tm.errors[i] = tm.feeds[i].Next()
			}
			wg.Done()
		}(i)
	}
	wg.Wait()

	postIdx := -1
	var firstPost *Post

	for i, post := range tm.posts {
		if post == nil {
			continue
		}

		if firstPost == nil || post.Date.After(firstPost.Date) {
			postIdx = i
			firstPost = post
		}
	}

	if firstPost == nil {
		return nil, fmt.Errorf("no more posts: %w", io.EOF)
	}

	tm.posts[postIdx] = nil
	firstPost.Author = tm.feeds[postIdx].Name()
	return firstPost, nil
}

func (tm *tumblrMerger) Close() error {
	var err error
	for _, t := range tm.feeds {
		err = t.Close()
	}
	return err
}

type Feed interface {
	Name() string
	URL() string
	Next() (*Post, error)
	Close() error
}

type FeedFn func(ctx context.Context, name string, search Search) (Feed, error)
type CacheFn func(ctx context.Context, name string, uncachedFn FeedFn, search Search) (Feed, error)

func NewCachedFeed(ctx context.Context, name string, cacheFn CacheFn, search Search) (Feed, error) {
	switch {
	case strings.HasSuffix(name, "@twitter"):
		return cacheFn(ctx, name, NewNitter, search)
	case strings.HasSuffix(name, "@instagram"):
		return cacheFn(ctx, name, NewBibliogram, search)
	case strings.Contains(name, "archiveofourown.org"):
		return cacheFn(ctx, name, NewAO3, search)
	case strings.Contains(name, "@") || strings.Contains(name, "."):
		return cacheFn(ctx, name, NewRSS, search)
	default:
		return cacheFn(ctx, name, NewTumblrRSS, search)
	}
}

func NewCached(ctx context.Context, name string, uncachedFn FeedFn, search Search) (Feed, error) {
	cached, isCached := cache.Get(name)
	if isCached && time.Since(cached.(*cachedFeed).cachedAt) < CacheTime {
		feed := *cached.(*cachedFeed)
		return &feed, nil
	}
	feed, err := uncachedFn(ctx, name, search)
	if err != nil {
		return nil, err
	}
	return &cachingTumblr{
		uncached: feed,
		cached: &cachedFeed{
			cachedAt: time.Now(),
			name:     name,
			url:      feed.URL(),
			posts:    make([]*Post, 0, 10),
		},
	}, nil
}

type cachingTumblr struct {
	uncached Feed
	cached   *cachedFeed
}

func (ct *cachingTumblr) Name() string {
	return ct.uncached.Name()
}

func (ct *cachingTumblr) URL() string {
	return ct.uncached.URL()
}

func (ct *cachingTumblr) Next() (*Post, error) {
	post, err := ct.uncached.Next()
	if err != nil {
		return nil, err
	}
	ct.cached.posts = append(ct.cached.posts, post)
	return post, nil
}

func (ct *cachingTumblr) Close() error {
	cache.Add(ct.uncached.Name(), ct.cached)
	return ct.uncached.Close()
}

type cachedFeed struct {
	cachedAt time.Time
	name     string
	url      string
	posts    []*Post
}

func (ct *cachedFeed) Name() string {
	return ct.name
}

func (ct *cachedFeed) URL() string {
	return ct.url
}

func (ct *cachedFeed) Next() (*Post, error) {
	if len(ct.posts) == 0 {
		return nil, fmt.Errorf("no more posts: %w", io.EOF)
	}

	post := ct.posts[0]
	ct.posts = ct.posts[1:]
	return post, nil
}

func (ct *cachedFeed) Close() error {
	return nil
}
