package main

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

func TestFlattenReblogs(t *testing.T) {
	reblogs := []string{
		`<p><a href="https://april-thelightfury115.tumblr.com/post/628962798765998080/lytefoot-vivithefolle-headcanonsandmore" class="tumblr_blog">april-thelightfury115</a>:</p> <blockquote><p><a href="https://lytefoot.tumblr.com/post/627529363045384192/vivithefolle-headcanonsandmore" class="tumblr_blog">lytefoot</a>:</p> <blockquote> <p><a href="https://vivithefolle.tumblr.com/post/627528961548795904/headcanonsandmore-evitoxytrash-i-found-these" class="tumblr_blog">vivithefolle</a>:</p> <blockquote> <p><a href="https://headcanonsandmore.tumblr.com/post/627528598568435712/evitoxytrash-i-found-these-in-my-notes-and" class="tumblr_blog">headcanonsandmore</a>:</p> <blockquote> <p><a href="https://evitoxytrash.tumblr.com/post/627470558410555392/i-found-these-in-my-notes-and-honestly-they-are" class="tumblr_blog">evitoxytrash</a>:</p> <blockquote> <p>I found these in my notes, and honestly, they are pure gold…</p> <p><br/></p> <p>—</p> <p>Teddy, into a hairbrush: YOOOOOOO I’ll tell you what I want, what I really really want</p> <p>Harry, into a different hairbrush: So tell me what you want what you really really want</p> <p>Remus, walking into the room: Harry</p> <p>Remus: What the fuck have you done to my child</p> <p>—</p> <p>*3am* </p> <p>Percy: What is all that racket</p> <p>*ball hits the window* </p> <p>Percy: *looks out the window to see his dumbass husband hosting Quidditch practice for their children* </p> <p>Percy: OLIVER IT IS THREE IN THE FUCKING MORNING</p> <p>—</p> <p>*procession music starts playing* </p> <p>Hermione: *comes out in a tux* </p> <p>Molly: …</p> <p>Ron: *struts down the aisle in a wedding dress* </p> <p>Molly: RONALD</p> <p>-</p> <p>Lee: *puts his child in a crib while Fred films* </p> <p>Crib: *turns into a rubber chicken* </p> <p>Lee: lmao</p> <p>—</p> <p>Angelina: George, don’t you <i>dare</i> cause a piece of furniture to turn into a rubber chicken</p> <p>George, frantically disabling all the transfiguration charms he had put on the table and chairs: Why would I ever do that? </p> <p>—</p> <p>*procession music starts playing* </p> <p>Lee: *comes out in nice pajamas*</p> <p>Fred: *comes out in nice pajamas as well* </p> <p>Molly: FREDERICK</p> <p>—</p> <p>Charlie, writing a letter: Dear mum,</p> <p>Charlie: I don’t know why you’re asking me, since you have seven kids</p> <p>Charlie: But since you want grandbabies</p> <p>Charlie: Here you go</p> <p>Charlie: *sends a picture of a dragon in a diaper*</p> <p>Charlie: Love, Charlie</p> </blockquote> <p><b>I, for one, think Ron would look <i>amazing</i> in a wedding dress. </b></p> </blockquote> <p>We need more pics of Romione weddings with Ron in a wedding dress.</p> <p>Scratch that we need more pictures of Ron in general.</p> </blockquote> <p>All of this is frickin <i>gold</i>.</p> </blockquote> <p>YES</p></blockquote>`,
		`<p><a class="tumblr_blog" href="https://slytherco.tumblr.com/post/628881174844112896" target="_blank">slytherco</a>:</p><blockquote><figure class="tmblr-full" data-orig-height="2048" data-orig-width="1310"><img src="https://64.media.tumblr.com/683043a5a4c233c57fb42777dc44d713/65e71f4f39b89922-c9/s640x960/c8b047d310aa9b761e4d7f8e618822a7d04d1b1b.png" data-orig-height="2048" data-orig-width="1310"/></figure><p>I drew a naked Draco as a gift for <a class="tumblelog" href="https://tmblr.co/mijEE2qDDKca6_nfIAcd3mw" target="_blank">@shealwaysreads</a> because she deserves the world and all the naked, pensive boys. Enjoy him, babes. 💕💕</p><p>The fur is either fake or thrifted, obviously.</p><p>I hope you like him, it&rsquo;s my first time trying to colour kinda-gold, will try to improve.</p><p>[<a href="http://slytherco.tumblr.com/tagged/my+art" target="_blank">my other art</a>]</p></blockquote><p>This is so cool!! The textures are just wow! Your style really shines here (yes pun intended!)</p>`,
	}

	for _, reblog := range reblogs {
		t.Run(reblog, func(t *testing.T) {
			flattened, err := FlattenReblogs(reblog)
			require.NoError(t, err, "flatten")

			_, err = html.Parse(strings.NewReader(flattened))
			require.NoError(t, err, "parse flattened html")

			original, err := html.Parse(strings.NewReader(reblog))
			require.NoError(t, err, "parse reblog html")

			// ensure all text from original is part of the flattened html
			var visit func(*html.Node)
			visit = func(node *html.Node) {
				if node.Type == html.TextNode {
					require.Contains(t, flattened, node.Data, "missing text")
				}

				for child := node.FirstChild; child != nil; child = child.NextSibling {
					visit(child)
				}
			}
			visit(original)

			f, err := os.OpenFile("reblog-test.html", os.O_TRUNC|os.O_RDWR|os.O_CREATE, 0644)
			require.NoError(t, err, "open reblog-test.html")
			defer f.Close()

			f.Write([]byte(flattened))
		})
	}
}
