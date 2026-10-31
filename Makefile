GO ?= go
GOARCH ?= amd64
VERSION ?= debug
BUILDVCS ?= false

ifeq ($(OS),Windows_NT)
GOENV_WINDOWS = set GOOS=windows&& set GOARCH=$(GOARCH)&&
GOENV_LINUX = set GOOS=linux&& set GOARCH=$(GOARCH)&&
else
GOENV_WINDOWS = GOOS=windows GOARCH=$(GOARCH)
GOENV_LINUX = GOOS=linux GOARCH=$(GOARCH)
endif

LDFLAGS = -X main.version=$(VERSION)

WINDOWS_KRUN_BIN ?= krun.exe
LINUX_KRUN_BIN ?= krun
WINDOWS_HELPER_BIN ?= krun-helper.exe
LINUX_HELPER_BIN ?= krun-helper


.PHONY: build-all build-windows build-linux clean \
	build-krun-windows build-krun-linux \
	build-helper-windows build-helper-linux

build-all: build-windows build-linux

build-windows: build-krun-windows build-helper-windows

build-linux: build-krun-linux build-helper-linux

build-krun-windows:
	$(GOENV_WINDOWS) $(GO) build -ldflags "$(LDFLAGS)" -buildvcs=$(BUILDVCS) -o $(WINDOWS_KRUN_BIN) ./cmd/krun

build-krun-linux:
	$(GOENV_LINUX) $(GO) build -ldflags "$(LDFLAGS)" -buildvcs=$(BUILDVCS) -o $(LINUX_KRUN_BIN) ./cmd/krun

# krun-helper.exe carries its asInvoker UAC manifest via the
# committed cmd/krun-helper/resource_windows_amd64.syso, which the Go linker
# embeds automatically. No post-link mt.exe step, so this cross-compiles.
build-helper-windows:
	$(GOENV_WINDOWS) $(GO) build -ldflags "$(LDFLAGS)" -buildvcs=$(BUILDVCS) -o $(WINDOWS_HELPER_BIN) ./cmd/krun-helper

build-helper-linux:
	$(GOENV_LINUX) $(GO) build -ldflags "$(LDFLAGS)" -buildvcs=$(BUILDVCS) -o $(LINUX_HELPER_BIN) ./cmd/krun-helper

clean:
	-rm -f $(WINDOWS_KRUN_BIN) $(LINUX_KRUN_BIN) $(WINDOWS_HELPER_BIN) $(LINUX_HELPER_BIN)
