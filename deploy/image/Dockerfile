FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata \
    && mkdir -p /app /data \
    && chown 10001:10001 /app /data
COPY --chmod=0555 build/cliproxyapi /app/cliproxyapi
ARG VCS_REF
LABEL org.opencontainers.image.source="https://github.com/BppleMan/CLIProxyAPI" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.title="CLIProxyAPI private RP"
USER 10001:10001
WORKDIR /data
ENV TZ=Asia/Shanghai CPA_PRIVATE_RP=1
EXPOSE 8317
ENTRYPOINT ["/app/cliproxyapi"]
CMD ["-config", "/data/config.yaml", "-local-model"]
