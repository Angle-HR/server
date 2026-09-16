FROM golang:1.25.10-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -o /email-worker ./cmd/email-worker

FROM alpine:3.21 AS server

RUN apk add --no-cache ca-certificates wget

COPY --from=builder /server /server

EXPOSE 8080

ENTRYPOINT ["/server"]

FROM alpine:3.21 AS email-worker

RUN apk add --no-cache ca-certificates

COPY --from=builder /email-worker /email-worker

ENTRYPOINT ["/email-worker"]
