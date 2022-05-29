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

## Filtering and blocking

It is possible to filter feeds using the same syntax for searches, but either
specific to single feeds or applying to all feeds at once.

Generally the syntax is like this:

    my-feed -wordidontwanttosee
    my-feed -#tagidontwanttosee

Unfortunately this only works for single words so far, so filtering multi-word
phrases or tags does not work yet.

Note that by default posts are hidden like tumblr does with a note about which
filter has hidden this.  However, if you want to remove a post completely
without you even knowing that it used to exist you can add `skip` to the
filter:

    my-feed -neverthisword skip

That will remove posts that contain the phrase "neverthisword" completely from
your feed.

Here's a few concrete examples:

- [staff -tipping](/staff -tipping)

    This hides posts from staff that contain the word "tipping".
- [staff -#features](/staff -%23features)

    This hides posts from staff that have the tag `#features`.

    Note that if you specify this via the URL manually you will need to encode
    the `#` so that it can be part of a URL.  The escape code for that is `%23`
    as used in the link.  This also applies to the `?feeds=...` parameter, but
    not to setting the filter using the settings.

## There's more ...

... and unfortunately it is not documented yet.  If you find something you
like, do feel free to contribute at
<https://github.com/heyLu/numblr/edit/main/help.md>.
