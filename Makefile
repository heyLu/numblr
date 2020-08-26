.PHONY: lighthouse.html

numbl: *.go
	CGO_ENABLED=0 go build .
	strip numbl
	upx numbl

lighthouse: lighthouse.html

lighthouse.html:
	lighthouse --chrome-flags='--headless' --output-path=lighthouse.html http://localhost:5555
