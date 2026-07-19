# syntax=docker/dockerfile:1.7
ARG BASE_IMAGE=alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40

FROM ${BASE_IMAGE} AS packages
RUN apk add --no-cache ca-certificates squid

FROM packages AS runtime
RUN mkdir -p /var/log/squid /var/spool/squid \
    && chown -R 31:31 /var/log/squid /var/spool/squid
COPY --chmod=0444 squid.conf /etc/squid/squid.conf
USER 31:31
EXPOSE 3128
ENTRYPOINT ["/usr/sbin/squid"]
CMD ["-N", "-f", "/etc/squid/squid.conf"]
