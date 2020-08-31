.PHONY: lighthouse.html

numbl: *.go
	CGO_ENABLED=0 go build .
	strip numbl
	upx numbl

reload_run:
	git ls-files --cached --others | grep -v '_test.go$$' | grep '.go$$' | entr -c -r go run .

reload_test:
	git ls-files --cached --others | grep -v '_test.go$$' | grep '.go$$' | entr -c -r go test .

lighthouse: lighthouse.html

lighthouse.html:
	lighthouse --chrome-flags='--headless' --output-path=lighthouse.html http://localhost:5555
