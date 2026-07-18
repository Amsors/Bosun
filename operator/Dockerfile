ARG GO_IMAGE=golang:1.26.2-alpine3.23@sha256:f85330846cde1e57ca9ec309382da3b8e6ae3ab943d2739500e08c86393a21b1
ARG RUNTIME_IMAGE=alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40

FROM ${GO_IMAGE} AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/operator ./cmd

FROM ${RUNTIME_IMAGE}
RUN addgroup -g 10001 bosun && adduser -D -H -u 10001 -G bosun bosun
COPY --from=builder /out/operator /usr/local/bin/operator
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/operator"]
