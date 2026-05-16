FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o savras ./cmd/savras

FROM gcr.io/distroless/static-debian12

COPY --from=builder /app/savras /savras
COPY --from=builder /app/config.example.yaml /etc/savras/config.yaml

EXPOSE 8080

ENTRYPOINT ["/savras"]
