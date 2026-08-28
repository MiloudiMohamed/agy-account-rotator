BINARY := agy-rotator
PREFIX := $(HOME)/.local

.PHONY: build test vet fmt install clean

build:
	go build -trimpath -ldflags "-s -w" -o bin/$(BINARY) ./cmd/$(BINARY)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

install: build
	install -m 0755 bin/$(BINARY) $(PREFIX)/bin/$(BINARY)
	$(PREFIX)/bin/$(BINARY) shim install --write-rc || true
	$(PREFIX)/bin/$(BINARY) plugin install || true
	$(PREFIX)/bin/$(BINARY) completions install || true

clean:
	rm -rf bin dist
