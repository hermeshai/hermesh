# hermesh-relay: self-hosted, untrusted coordinator (README: Run your own server).
#   docker run -d --name hermesh-relay --restart unless-stopped -p 80:80 -p 443:443 \
#     -v hermesh-relay:/data -e HERMESH_RELAY_DOMAIN=relay.example.com -e HERMESH_RELAY_MCP=1 \
#     hermeshai/hermesh-relay
# Without HERMESH_RELAY_DOMAIN it serves plain HTTP on :8080, for a proxy
# that serves HTTPS (then set HERMESH_RELAY_PUBLIC_URL).
# For Computer Control where the two ends don't reach each other directly, add
#   -p 3478:3478/udp -p 3478:3478/tcp -p 49160-49200:49160-49200/udp \
#   -e HERMESH_RELAY_TURN_LISTEN=:3478 -e HERMESH_RELAY_TURN_PORTS=49160-49200
# (docs/computer-control.md §7).
# Built on the build platform for each target (linux/amd64, linux/arm64).
FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
COPY third_party/pion-ice ./third_party/pion-ice
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/hermesh-relay ./cmd/hermesh-relay && mkdir /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/hermesh-relay /usr/local/bin/hermesh-relay
# The relay runs as nonroot: a new volume takes this directory's owner.
COPY --from=build --chown=65532:65532 /out/data /data
# docker exec hermesh-relay hermesh-relay claim-code (or mcp-url) finds it too.
ENV HERMESH_RELAY_DATA=/data
VOLUME /data
EXPOSE 80 443 8080
ENTRYPOINT ["/usr/local/bin/hermesh-relay"]
CMD ["serve"]
