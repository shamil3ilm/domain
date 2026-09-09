# --- build ---
FROM golang:1.23-alpine AS build
WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum* ./
RUN go mod download || true

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# --- runtime ---
FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget && adduser -D -H -s /sbin/nologin api
COPY --from=build /out/api /usr/local/bin/api
USER api
EXPOSE 8080
CMD ["/usr/local/bin/api"]
