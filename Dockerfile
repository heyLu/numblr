FROM docker.io/golang:1.23-alpine3.21 as builder

# gcc and libc-dev for sqlite, git for vcs listing in /stats page
RUN apk add --no-cache gcc libc-dev git

WORKDIR /build

COPY . .
RUN go build .

FROM alpine:3.21

RUN apk add --no-cache shadow && useradd --home-dir /dev/null --shell /bin/false numblr && apk del shadow
USER numblr

VOLUME /app/data

CMD /app/numblr -addr 0.0.0.0:5555

COPY --from=builder /build/numblr /app/
