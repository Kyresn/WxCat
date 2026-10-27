VERSION ?= dev
IMAGE ?= wxcat
FULL_IMAGE ?= wxcat-wcf
PLATFORMS ?= linux/amd64,linux/arm64

.PHONY: test build image image-full

test:
	go test ./...

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X wxcat/internal/onebot.Version=$(VERSION)" -o bin/wxcat ./cmd/wxcat
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/wxcat-agent ./cmd/wcf-agent

image:
	docker buildx build --platform $(PLATFORMS) -f docker/Dockerfile \
		--build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) .

image-full:
	docker buildx build --platform linux/amd64 -f docker/Dockerfile.wcf \
		--build-arg VERSION=$(VERSION) -t $(FULL_IMAGE):$(VERSION) .
