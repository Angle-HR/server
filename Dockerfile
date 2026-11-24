FROM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN for job in hiring-expire hiring-outbox hiring-retention hiring-uploads-sweep kyb-sweep; do \
		CGO_ENABLED=0 GOOS=linux go build -o /jobs/$job ./cmd/$job || exit 1; \
	done

# Scheduled one-shot binaries. No ENTRYPOINT: each ECS task sets command = ["/usr/local/bin/<name>"].
FROM alpine:3.21 AS jobs

RUN apk add --no-cache ca-certificates

COPY --from=builder /jobs/ /usr/local/bin/
