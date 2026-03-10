FROM golang:1.25.5 AS builder
WORKDIR /src
ARG GIT_VERSION=debug

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=${GIT_VERSION}" -o /out/traffic-manager ./cmd/traffic-manager

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/traffic-manager /traffic-manager
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/traffic-manager"]
