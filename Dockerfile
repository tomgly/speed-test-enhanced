FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY web.go index.html LICENSE ./
COPY assets ./assets
COPY internal ./internal
COPY cmd ./cmd
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /speedtest ./cmd/speedtest

FROM scratch
COPY --from=build /speedtest /speedtest
USER 65532:65532
EXPOSE 3000/tcp 3001/tcp 3002/udp
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 CMD ["/speedtest", "healthcheck"]
ENTRYPOINT ["/speedtest"]
CMD ["server"]
