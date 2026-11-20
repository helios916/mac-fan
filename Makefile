BIN := mac-fan
GO ?= go

.PHONY: all build sign clean install-user

all: $(BIN)

$(BIN): $(wildcard *.go) go.mod
	CGO_ENABLED=1 $(GO) build -trimpath -ldflags "-s -w" -o $(BIN) .
	codesign --force --sign - --options runtime $(BIN) >/dev/null 2>&1 || true

build:
	CGO_ENABLED=1 $(GO) build -trimpath -ldflags "-s -w" -o $(BIN) .

sign:
	codesign --force --sign - --options runtime $(BIN)

install-user: build
	mkdir -p $(HOME)/.local/bin
	cp $(BIN) $(HOME)/.local/bin/$(BIN)

clean:
	rm -f $(BIN)
