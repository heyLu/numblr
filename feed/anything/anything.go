package anything

import (
	"context"
	"strings"

	"github.com/heyLu/numblr/feed"
	"github.com/heyLu/numblr/feed/ao3"
	"github.com/heyLu/numblr/feed/bibliogram"
	"github.com/heyLu/numblr/feed/nitter"
	"github.com/heyLu/numblr/feed/rss"
	"github.com/heyLu/numblr/feed/tiktok"
	"github.com/heyLu/numblr/feed/tumblr"
	"github.com/heyLu/numblr/feed/youtube"
)

// Open any supported feed by name, depending on name, suffix or even full
// urls.
func Open(ctx context.Context, name string, cacheFn feed.OpenCached, search feed.Search) (feed.Feed, error) {
	switch {
	case strings.HasSuffix(name, "@twitter") || strings.HasSuffix(name, "@t"):
		return cacheFn(ctx, name, nitter.Open, search)
	case strings.HasSuffix(name, "@instagram") || strings.HasSuffix(name, "@ig"):
		return cacheFn(ctx, name, bibliogram.Open, search)
	case strings.HasSuffix(name, "@youtube") || strings.HasSuffix(name, "@yt"):
		return cacheFn(ctx, name, youtube.Open, search)
	case strings.HasSuffix(name, "@tumblr"):
		return cacheFn(ctx, name, tumblr.Open, search)
	case strings.Contains(name, "www.tiktok.com") || strings.HasSuffix(name, "@tiktok"):
		return cacheFn(ctx, name, tiktok.Open, search)
	case strings.Contains(name, "archiveofourown.org") || strings.HasSuffix(name, "@ao3"):
		return cacheFn(ctx, name, ao3.Open, search)
	case strings.Contains(name, "@") || strings.Contains(name, "."):
		return cacheFn(ctx, name, rss.Open, search)
	default:
		return cacheFn(ctx, name, tumblr.Open, search)
	}
}
