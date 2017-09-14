FROM golang:1.9-alpine

COPY . $GOPATH/src/github.com/gojp/goreportcard

WORKDIR $GOPATH/src/github.com/gojp/goreportcard

RUN apk update && apk upgrade && apk add --no-cache git make \
        && go get golang.org/x/tools/go/vcs \
        && ./scripts/make-install.sh \
        && go build

EXPOSE 8000

ENTRYPOINT ["./goreportcard"]
