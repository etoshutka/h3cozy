FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o main-binary ./instances/server.go

FROM alpine:3.17
COPY --from=builder /app/main-binary /usr/local/bin/main-binary
COPY cert.pem cert.pem
COPY key.pem key.pem
ENTRYPOINT ["main-binary"]
EXPOSE 4001
