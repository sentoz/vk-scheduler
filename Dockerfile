FROM alpine:3.21 AS certs
# Keep CA and timezone security updates from the selected Alpine release.
# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates tzdata && update-ca-certificates

FROM scratch
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=certs /usr/share/zoneinfo /usr/share/zoneinfo
ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
ENV ZONEINFO=/usr/share/zoneinfo
COPY --chmod=755 build/vk-scheduler-linux-amd64 /vk-scheduler
USER 65532:65532
ENTRYPOINT ["/vk-scheduler"]
