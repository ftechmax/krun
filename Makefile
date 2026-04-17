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


.PHONY: build-all build-windows build-linux clean lint lint-fix \
	build-krun-windows build-krun-linux build-krun-cross \
	build-helper-windows build-helper-linux build-helper-cross patch-helper-windows-uac

build-all: build-windows build-linux

build-windows: build-krun-windows patch-helper-windows-uac

build-linux: build-krun-linux build-helper-linux

build-krun-windows:
	$(GOENV_WINDOWS) $(GO) build -ldflags "$(LDFLAGS)" -buildvcs=$(BUILDVCS) -o $(WINDOWS_KRUN_BIN) ./cmd/krun

build-krun-linux:
	$(GOENV_LINUX) $(GO) build -ldflags "$(LDFLAGS)" -buildvcs=$(BUILDVCS) -o $(LINUX_KRUN_BIN) ./cmd/krun

build-krun-cross: build-krun-windows build-krun-linux

build-helper-windows:
	$(GOENV_WINDOWS) $(GO) build -buildvcs=$(BUILDVCS) -o $(WINDOWS_HELPER_BIN) ./cmd/krun-helper

build-helper-linux:
	$(GOENV_LINUX) $(GO) build -buildvcs=$(BUILDVCS) -o $(LINUX_HELPER_BIN) ./cmd/krun-helper

build-helper-cross: build-helper-windows build-helper-linux

patch-helper-windows-uac: build-helper-windows
	powershell -NoProfile -ExecutionPolicy Bypass -File ./scripts/patch-helper-windows.ps1

clean:
	-rm -f $(WINDOWS_KRUN_BIN) $(LINUX_KRUN_BIN) $(WINDOWS_HELPER_BIN) $(LINUX_HELPER_BIN)

lint:
	golangci-lint run --timeout=5m

lint-fix:
	golangci-lint run --fix --timeout=5m
