FROM golang:1.9-alpine

ARG GITHUB_USERNAME=
ARG GITHUB_PASSWORD=

COPY . $GOPATH/src/github.com/gojp/goreportcard

WORKDIR $GOPATH/src/github.com/gojp/goreportcard

RUN apk update && apk upgrade && apk add --no-cache git make \
        && go get golang.org/x/tools/go/vcs \
        && ./scripts/make-install.sh \
        && go build \
        && git config --global url."https://${GITHUB_USERNAME}:${GITHUB_PASSWORD}@github.com/".insteadOf "https://github.com/"

EXPOSE 8000

ENTRYPOINT ["./goreportcard"]
