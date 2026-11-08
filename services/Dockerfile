# syntax=docker/dockerfile:1
FROM golang:1.22-alpine AS build
ARG SERVICE
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY pkg ./pkg
COPY services ./services
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/app ./services/${SERVICE}

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H -u 65532 nonroot
COPY --from=build /out/app /app
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/app"]
