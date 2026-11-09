FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ralsei .

FROM alpine:3.21
RUN addgroup -g 1000 ralsei && adduser -D -u 1000 -G ralsei ralsei
COPY --from=build /out/ralsei /usr/local/bin/ralsei
USER 1000:1000
WORKDIR /data
ENTRYPOINT ["/usr/local/bin/ralsei"]
