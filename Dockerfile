# Build del bot (multi-stage, sin CGO: modernc.org/sqlite es Go puro)
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bot .

# Imagen final mínima
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 1000 bot
WORKDIR /app
COPY --from=build /out/bot /app/bot
RUN mkdir -p /app/data && chown -R bot:bot /app
USER bot
EXPOSE 8080
VOLUME ["/app/data"]
ENTRYPOINT ["/app/bot"]
