FROM --platform=$BUILDPLATFORM golang:1.25.5 AS builder
WORKDIR /src
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG GIT_VERSION=debug

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${GIT_VERSION}" -o /out/traffic-agent ./cmd/traffic-agent

FROM alpine:3.23
RUN apk add --no-cache iptables
COPY --from=builder /out/traffic-agent /traffic-agent
EXPOSE 8081
ENTRYPOINT ["/traffic-agent"]