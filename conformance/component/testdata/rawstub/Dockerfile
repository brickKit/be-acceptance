# conformance/rawstub: plain Go, no be-sdk. BROKEN=<variant> (broken-variants.yaml) breaks one rule.
FROM golang:1.25-alpine AS build
ARG BROKEN=""
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-mod=mod
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY migrations/ ./migrations/
COPY platform/ ./platform/
RUN go build -trimpath -ldflags "-s -w -X main.broken=${BROKEN}" -o /out/rawstub .

# alpine has /bin/sh and busybox wget for the platform's health check (P1.9).
FROM alpine:3
RUN adduser -D -u 10001 rawstub
COPY --from=build /out/rawstub /app/rawstub
USER rawstub
EXPOSE 8080 9090
ENTRYPOINT []
CMD ["/app/rawstub"]
