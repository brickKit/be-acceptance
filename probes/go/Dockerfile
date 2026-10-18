# The Go probe alone: a static binary on Alpine (the health check runs through /bin/sh and wget).
FROM golang:1.27.2-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /probe-go .

FROM alpine:3.24.2
COPY --from=build /probe-go /probe-go
# the listening port in a container; BrickKit injects PORT only for mode: local
ENV PORT=8600
EXPOSE 8600 9600
ENTRYPOINT ["/probe-go"]
