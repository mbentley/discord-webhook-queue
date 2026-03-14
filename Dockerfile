FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG TARGETOS=linux
ARG TARGETARCH

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -trimpath -o discord-webhook-queue .

FROM mbentley/alpine:latest

RUN apk add --no-cache ca-certificates
COPY --from=builder /build/discord-webhook-queue /usr/local/bin/discord-webhook-queue

USER 523:523
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["discord-webhook-queue"]