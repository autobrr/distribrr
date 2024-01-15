SERVICE = distribrr

GIT_COMMIT := $(shell git rev-parse HEAD 2> /dev/null)
GIT_TAG := $(shell git tag --points-at HEAD 2> /dev/null | head -n 1)

GO ?= go
RM ?= rm
GOFLAGS ?= "-s -w -extldflags=-static -X version.Commit=$(GIT_COMMIT) -X version.Version=dev"
PREFIX ?= /usr/local
BINDIR ?= bin

GIT_COMMIT := $(shell git rev-parse HEAD 2> /dev/null)
GIT_TAG := $(shell git tag --points-at HEAD 2> /dev/null | head -n 1)

all: clean build

deps:
	go mod download

build: deps
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags $(GOFLAGS) -o bin/distribrr cmd/distribrr/main.go
	chmod +x bin/distribrr

#build/docker:
#	docker build -t distribrr:dev -f Dockerfile . --build-arg GIT_TAG=$(GIT_TAG) --build-arg GIT_COMMIT=$(GIT_COMMIT)

clean:
	$(RM) -rf bin
