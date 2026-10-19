BIN := bin

PREFIX ?= $(HOME)/.local

.PHONY: build build-darwin helper install run test fmt clean

build:
	go build -o $(BIN)/prgif .

# Cross-compile for Apple Silicon; run the result natively on the Mac.
build-darwin:
	GOOS=darwin GOARCH=arm64 go build -o $(BIN)/prgif-darwin-arm64 .

# Build the drag-to-select overlay. Run this on the Mac: it needs AppKit and
# swiftc from the Xcode Command Line Tools (xcode-select --install).
helper:
	@if [ "$$(uname)" != Darwin ]; then echo "make helper must run on macOS (it needs AppKit and swiftc)"; exit 1; fi
	mkdir -p $(BIN)
	swiftc -O helper/select-region.swift -o $(BIN)/select-region

# Install prgif and the helper side by side on the Mac. Build them first:
# make build-darwin (dev container) and make helper (Mac).
install:
	@if [ "$$(uname)" != Darwin ]; then echo "make install must run on macOS"; exit 1; fi
	@if [ ! -x $(BIN)/prgif-darwin-arm64 ]; then echo "$(BIN)/prgif-darwin-arm64 is missing: run make build-darwin in the dev container"; exit 1; fi
	@if [ ! -x $(BIN)/select-region ]; then echo "$(BIN)/select-region is missing: run make helper"; exit 1; fi
	install -d $(PREFIX)/bin
	install -m 755 $(BIN)/prgif-darwin-arm64 $(PREFIX)/bin/prgif
	install -m 755 $(BIN)/select-region $(PREFIX)/bin/select-region
	@case ":$$PATH:" in *":$(PREFIX)/bin:"*) ;; *) echo "Note: $(PREFIX)/bin is not on your PATH; add it to run prgif from anywhere." ;; esac

run:
	go run .

test:
	go test ./...

fmt:
	gofumpt -w .

clean:
	rm -rf $(BIN)
