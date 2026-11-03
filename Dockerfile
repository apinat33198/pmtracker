# Build with a supported Go release: Go 1.24 and 1.25 no longer get security fixes.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pmtracker ./cmd/pmtracker

# distroless/static has CA certificates for the Google API; time zones are
# embedded in the binary.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/pmtracker /pmtracker
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/pmtracker"]
CMD ["-config", "/config/config.json", "-data", "/data/pmtracker.json"]
