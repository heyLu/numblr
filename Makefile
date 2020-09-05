.PHONY: lighthouse.html

numbl: favicon_png.go *.go Makefile
	go build .
	strip numbl
	upx numbl

favicon_png.go: favicon.png embed.rb Makefile
	./embed.rb favicon.png FaviconPNGBytes favicon_png.go

favicon.png: favicon.svg
	inkscape --export-width=192 --export-height=192 favicon.svg -o favicon.png

reload_run:
	git ls-files --cached --others | grep -v '_test.go$$' | grep '.go$$' | entr -c -r go run .

reload_run_db:
	git ls-files --cached --others | grep -v '_test.go$$' | grep '.go$$' | entr -c -r go run . -addr localhost:5556 -db cache.db

reload_test:
	git ls-files --cached --others | grep -v '_test.go$$' | grep '.go$$' | entr -c -r go test .

lighthouse:
	lighthouse --chrome-flags='--headless' --output-path=lighthouse.html http://localhost:5555

lighthouse_url:
	lighthouse --chrome-flags='--headless' --output-path=lighthouse_url.html "$(URL)"
