FROM golang:1.25-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/server .

FROM alpine:3.20
RUN apk add --no-cache wget
COPY --from=build /out/server /app/server
COPY component.yaml /app/component.yaml
EXPOSE 19003
ENTRYPOINT ["/app/server"]
