# ---- build: compila o hub em um binário estático (e roda os testes)
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /hub .

# ---- runtime: alpine + ffmpeg (o transcoder roda dentro do hub)
FROM alpine:3.23
# volumes nomeados herdam o dono de /hls e /data na primeira montagem
RUN apk add --no-cache ffmpeg ca-certificates tzdata \
 && adduser -D -u 10001 hub \
 && mkdir -p /hls /data && chown hub:hub /hls /data
COPY --from=build /hub /usr/local/bin/hub
USER hub
EXPOSE 8000
ENTRYPOINT ["/usr/local/bin/hub"]
