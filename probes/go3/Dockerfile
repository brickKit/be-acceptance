# The Go probe built at its release tag (the code lives in probes/go); COMPONENT_ID picks this variant.
FROM golang:1.27.2-alpine AS build
RUN CGO_ENABLED=0 GOBIN=/out go install -trimpath github.com/brickKit/be-acceptance/probes/go/v3@v3.2.16

FROM alpine:3.24.2
COPY --from=build /out/go /probe-go
ENV PORT=8603
EXPOSE 8603 9603
ENTRYPOINT ["/probe-go"]
