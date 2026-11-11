ARG GO_VERSION
FROM golang:${GO_VERSION}-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN go build -trimpath -ldflags="-s -w -X github.com/nais/nais-github-teamsync/internal/cmd/reconciler.version=${VERSION}" -o /nais-github-teamsync .

FROM gcr.io/distroless/base:nonroot
WORKDIR /app
COPY --from=builder /nais-github-teamsync /app/nais-github-teamsync
USER 65532:65532
EXPOSE 3105
ENTRYPOINT ["/app/nais-github-teamsync"]
