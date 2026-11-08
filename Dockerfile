# Used by GoReleaser: the prebuilt, static dsui-agent binary is in the build context.
# For a local image run `make docker`.
FROM gcr.io/distroless/static-debian12:nonroot

COPY dsui-agent /usr/local/bin/dsui-agent

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/dsui-agent"]
CMD ["run"]
