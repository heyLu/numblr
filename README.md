# numblr

Alternative Tumblr (and Twitter, Instagram, RSS, ...) frontend.

Very scrappy, but usable and useful for its original author.  Please
host your own!

Inspired by [nitter](https://github.com/zedeus/nitter).

Does not store any personal data on the server, subscriptions and
settings are only optionally stored in a cookie in your browser.

![screenshot of the main page](./screenshot.png)

## Features

- ✓ rss and atom
- ✓ tumblr (via rss)
- ✓ twitter (via [nitter](https://github.com/zedeus/nitter))
- ✓ instagram (via [bibliogram](https://sr.ht/~cadence/bibliogram))
- ✓ mastodon (via rss)
- ✓ in-memory cache
- ✓ optional database cache
- ✓ native dark mode
- ✓ settings stored in cookie
- ✓ lists

## Building

1. run `make` to fetch dependencies and compile
2. start using `./numblr` and visit <http://localhost:5555>

## License

This project is licensed under AGPLv3.
