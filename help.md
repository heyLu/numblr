# hjälp (help)

`numblr` is an alternative Tumblr (and Twitter, Instagram, AO3, YouTube, RSS, ...) frontend.

It is also rather scrappy, so beware some bugs and eldritch features.

## Basic usage

`numblr` mirrors Tumblr and other sources.  For example, to display the content
of <https://staff.tumblr.com> on `numblr`, you would navigate to
[`/staff`](/staff).

For the other sources, there is special syntax to specify which source you want:

- For Twitter, you use the `@twitter` (or `@t`) suffix.

  [`/lilnasx@twitter`](/lilnasx@twitter) gives you the content of
  <https://twitter.com/LilNasX>.
- For Instagram, you use the `@instagram` (or `@ig`) suffix.

  [`/lilnasx@instagram`](/lilnasx@instagram) gives you the content of
  <https://instagram.com/lilnasx>.
- For AO3 (Archive of our Own), you use the `@ao3` suffix.

  [`/astolat@ao3`](/astolat@ao3) gives you the content of
  <https://archiveofourown.org/users/astolat/works>.

  Direct links to AO3 urls like the following also work:

  - [`/?feeds=https://archiveofourown.org/users/astolat/works`](/?feeds=https://archiveofourown.org/users/astolat/works)
  - [`/?feeds=https://archiveofourown.org/users/astolat/works%3Ffandom_id=136512%26work_search[query]=heal`](/?feeds=https://archiveofourown.org/users/astolat/works%3Ffandom_id=136512%26work_search[query]=heal)
- For YouTube, you use the `@youtube` (or `@yt`) suffix.

  [`/lil%20nas%20x@youtube`](/lil%20nas%20x@youtube) gives you the content of
  <https://twitter.com/LilNasX>.
- And for good old [RSS](https://en.wikipedia.org/wiki/RSS), you use any name with a dot in it.

  [`/staff.tumblr.com`](/staff.tumblr.com) gives you the content of
  <https://staff.tumblr.com>.

  That example is a bit of fun because of the built-in Tumblr support, but you
  can also use any other site that provides an RSS feed, e.g.
  [`/wallflowerkitchen.com`](/wallflowerkitchen.com).

### Some special cases

You may have noticed the `feeds=...` parameter used above, which can be used
about restrictions regarding URL characters.  If a URL does not work, try it
using `/?feeds=...`.

## There's more ...

... and unfortunately it is not documented yet.  If you find something you
like, do feel free to contribute at
<https://github.com/heyLu/numblr/edit/main/help.md>.
