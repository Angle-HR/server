FROM golang:1.25.10-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -o /worker ./cmd/worker

FROM alpine:3.21 AS server

RUN apk add --no-cache ca-certificates

COPY --from=builder /server /server

EXPOSE 8080

ENTRYPOINT ["/server"]

FROM alpine:3.21 AS worker

RUN apk add --no-cache ca-certificates

COPY --from=builder /worker /worker

ENTRYPOINT ["/worker"]
