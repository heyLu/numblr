package tumblr

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
		`<p><a class="tumblr_blog" href="https://sum-stuff13.tumblr.com/post/629258204002631681">sum-stuff13</a>:</p><blockquote><p><a class="tumblelog" href="https://tmblr.co/m9HCYkOsRynYxZCrU2SW-xw">@rorybutnotgilmore</a> </p><p><a class="tumblelog" href="https://tmblr.co/mV1McY9Paj-oKMv5AuDVTbw">@irisbellemoon</a> </p><p><a class="tumblr_blog" href="https://teamtrickster.tumblr.com/post/629257635786539008">teamtrickster</a>:</p><blockquote><p><span class="npf_color_ross">WE WILL ABSOLUTELY HELP THIS PERSON GET A DAGGER -Loki</span></p><p><a class="tumblr_blog" href="https://xspiderfanx.tumblr.com/post/629257528334860288">xspiderfanx</a>:</p><blockquote><p><a class="tumblr_blog" href="https://transzukostanblog.tumblr.com/post/629245937078927360">transzukostanblog</a>:</p><blockquote><p><a class="tumblr_blog" href="https://ab0masum.tumblr.com/post/629184557210566656">ab0masum</a>:</p><blockquote><p><a class="tumblr_blog" href="https://awkward-finger-guns.tumblr.com/post/629184248473124864">awkward-finger-guns</a>:</p><blockquote><p><a class="tumblr_blog" href="https://frustratedasatruar.tumblr.com/post/629183290750959616">frustratedasatruar</a>:</p><blockquote><p>So this post was originally made on September 11th 2020. I am reblogging on September 13th of the same year. At the time my computer first loaded this post it was at ten-thousand-one-hundred-and-eighty-two notes. By the time I’d scrolled down to it and chose to open it in a new tab so I could check when it was originally made, it had increased to 10,191 notes. When I noticed this as I was preparing to reblog, I reloaded the page and found that the number had reached 10,198.</p><p>What I’m sayig is that somewhere, someone’s mother is quite likely approaching the realization that they may actually be compelled to live up to their end of this little bargain.</p><p><a class="tumblr_blog" href="https://red-gay-tree.tumblr.com/post/629180469719728128">red-gay-tree</a>:</p><blockquote><p><a class="tumblr_blog" href="https://mama-dubh.tumblr.com/post/629155802454867968">mama-dubh</a>:</p><blockquote><p><a class="tumblr_blog" href="https://the-turtleduck-pond.tumblr.com/post/628994287286337536">the-turtleduck-pond</a>:</p><blockquote><p><a class="tumblr_blog" href="https://unfried-mouth-wheat.tumblr.com/post/628994053434933248">unfried-mouth-wheat</a>:</p><blockquote><p><a class="tumblr_blog" href="https://awkward-finger-guns.tumblr.com/post/628984617332064256">awkward-finger-guns</a>:</p><blockquote><p>YALYALLLL YALLLL YAL</p><p><br/></p><p>MY MOM SAID IF I COULD GET 100,000 NOTES I CAN GET A DAGGER</p><p><br/></p><p>PLEASE HELP ME</p><p>IM NOT ALLOWED TO REBLOG OR SPAM MY OWN POST SO HELP ME OUT GUSY</p><p>PLEASE I WANT A DAGGER</p></blockquote><p>this seems like a good cause</p></blockquote><p>Time to put my blog to interesting use.</p></blockquote><p>@tilltheendwilliwrite​ Can we help this lovely out? </p></blockquote><p><a class="tumblelog" href="https://tmblr.co/msUDMFBURtvB4bWig0dsWzQ">@bittergayvvitch</a> </p></blockquote><p>I am now about to hit reblog, but before I do I’m reloading the page one last time. In the time it has taken me to type this, the number has reached 10,205.</p></blockquote><p>were only 1/10 th of the way there but yeah, my mom is kinda scared now</p></blockquote><p>Haha yeah we&rsquo;re getting you a dagger.</p></blockquote><p>I approve of this cause</p></blockquote><p><a class="tumblelog" href="https://tmblr.co/mGJJ-_9S3jxHk9jFV6hVlSw">@teamtrickster</a> let&rsquo;s help this person get a dagger</p></blockquote><p><span class="npf_color_monica">YES!!! -Gabriel</span></p></blockquote><p>Guys come on pls. We gotta get her a dagger </p></blockquote>`,
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

			_, _ = f.Write([]byte(flattened))
		})
	}
}
