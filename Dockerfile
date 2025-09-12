# Multi-stage build for showoff server
# Build stage
FROM golang:1.24-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	go build -trimpath -ldflags "-s -w" -o /out/showoff-server ./cmd/server

# Final minimal image
FROM gcr.io/distroless/static:nonroot
WORKDIR /app
COPY --from=build /out/showoff-server /app/showoff-server
# (Optional) Retain templates directory for inspection / future non-embedded usage
#COPY --from=build /src/internal/web/templates /app/templates
#ENV SHOWOFF_TEMPLATES_DIR=/app/templates
USER nonroot:nonroot
EXPOSE 8080 9000 9001 9100
ENTRYPOINT ["/app/showoff-server"]
# Example flags (override via args / env in deployment):
#   /app/showoff-server -public :8080 -control :9000 -data :9001 -metrics :9100
