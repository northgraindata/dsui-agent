# Built by GoReleaser (dockers_v2): each platform's static binary sits in the
# build context under its platform path, for example linux/amd64/dsui-agent.
# For a local image run `make docker`.
FROM gcr.io/distroless/static-debian12:nonroot

ARG TARGETPLATFORM
COPY $TARGETPLATFORM/dsui-agent /usr/local/bin/dsui-agent

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/dsui-agent"]
CMD ["run"]
