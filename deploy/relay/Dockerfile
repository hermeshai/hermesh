# hermesh-relay: self-hosted, untrusted coordinator.
#   docker run -d --name hermesh-relay -p 443:443 -p 80:80 -v hermesh-relay:/data \
#     ghcr.io/hermeshai/hermesh-relay:latest serve --data-dir /data --domain relay.example.com
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/hermesh-relay ./cmd/hermesh-relay && mkdir /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/hermesh-relay /usr/local/bin/hermesh-relay
# The relay runs as nonroot: a new volume takes this directory's owner.
COPY --from=build --chown=65532:65532 /out/data /data
# docker exec hermesh-relay hermesh-relay claim-code (or mcp-url) finds it too.
ENV HERMESH_RELAY_DATA=/data
VOLUME /data
EXPOSE 80 443 8080
ENTRYPOINT ["/usr/local/bin/hermesh-relay"]
CMD ["serve", "--data-dir", "/data", "--listen", ":8080"]
