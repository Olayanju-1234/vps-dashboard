VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet fmt fmtcheck ci demo release clean

build:
	go build -o bin/vps-dashboard ./cmd/vps-dashboard

test:
	go test -race -count=1 ./...

vet:
	go vet ./...
	GOOS=linux GOARCH=arm64 go vet ./...

fmt:
	gofmt -w .

fmtcheck:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "run make fmt" && exit 1)

ci: fmtcheck vet test release

demo:
	go run ./cmd/vps-dashboard demo

# Static binaries for the server; no Go toolchain is needed there.
release:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/vps-dashboard-linux-arm64 ./cmd/vps-dashboard
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/vps-dashboard-linux-amd64 ./cmd/vps-dashboard

clean:
	rm -rf bin dist
