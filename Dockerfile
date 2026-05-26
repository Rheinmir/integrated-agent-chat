# Stage 1: Build
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /app

COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o /agent-server ./main.go

# Stage 2: Run
FROM alpine:3.19

RUN apk add --no-cache ca-certificates

WORKDIR /app

COPY --from=builder /agent-server /app/agent-server

RUN mkdir -p /data

EXPOSE 8080

ENV DB_PATH=/data/agent.db
ENV PORT=8080

CMD ["/app/agent-server"]
