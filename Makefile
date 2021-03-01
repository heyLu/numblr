.PHONY: lighthouse.html

numblr: favicon.png *.go Makefile
	go build .
	strip numblr
	upx numblr

favicon.png: favicon.svg
	inkscape --export-width=192 --export-height=192 favicon.svg -o favicon.png

reload_run:
	git ls-files --cached --others | grep -v '_test.go$$' | grep '.go$$' | entr -c -r go run . -addr :5555

reload_run_db:
	git ls-files --cached --others | grep -v '_test.go$$' | grep '.go$$' | entr -c -r go run . -addr :5556 -db cache.db

reload_test:
	git ls-files --cached --others | grep '.go$$' | entr -c -r go test .

tmux:
	tmux split-window -l 20 -c $(PWD) make reload_run
	tmux split-window -t1 -h -c $(PWD) make reload_run_db
	tmux split-window -t2 -h -c $(PWD) make reload_test

lighthouse:
	lighthouse --chrome-flags='--headless' --output-path=lighthouse.html http://localhost:5555

lighthouse_url:
	lighthouse --chrome-flags='--headless' --output-path=lighthouse_url.html "$(URL)"
