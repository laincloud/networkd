#!/bin/sh

dep ensure

docker run --rm -v $GOPATH:/go -e GOPATH=/go -e GOBIN=/go/src/github.com/laincloud/networkd/bin golang:1.9.2 go install github.com/laincloud/networkd
