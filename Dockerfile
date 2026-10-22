FROM golang:1.25.10-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -o /email-worker ./cmd/email-worker
RUN for job in hiring-expire hiring-outbox hiring-retention hiring-uploads-sweep kyb-sweep; do \
		CGO_ENABLED=0 GOOS=linux go build -o /jobs/$job ./cmd/$job || exit 1; \
	done

FROM alpine:3.21 AS server

RUN apk add --no-cache ca-certificates wget

COPY --from=builder /server /server

EXPOSE 8080

ENTRYPOINT ["/server"]

FROM alpine:3.21 AS email-worker

RUN apk add --no-cache ca-certificates

COPY --from=builder /email-worker /email-worker

ENTRYPOINT ["/email-worker"]

# Scheduled one-shot binaries. No ENTRYPOINT: each ECS task sets command = ["/usr/local/bin/<name>"].
FROM alpine:3.21 AS jobs

RUN apk add --no-cache ca-certificates

COPY --from=builder /jobs/ /usr/local/bin/
