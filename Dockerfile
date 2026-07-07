# syntax=docker/dockerfile:1
FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-X github.com/gnanam1990/sieve/internal/version.Version=${VERSION}" -o sieve ./cmd/sieve

FROM alpine:latest
RUN apk add --no-cache ca-certificates cosign
WORKDIR /app
COPY --from=builder /src/sieve /usr/local/bin/sieve
ENV SIEVE_HOST=0.0.0.0
ENV SIEVE_PORT=8080
EXPOSE 8080
ENTRYPOINT ["sieve"]
CMD ["serve"]
