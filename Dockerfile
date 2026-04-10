FROM golang:1.26.2-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /controller ./cmd/controller/

FROM gcr.io/distroless/static-debian13
# FROM alpine:latest
COPY --from=builder /controller /controller
ENTRYPOINT ["/controller"]
