# bench/Dockerfile — runs the gopress load benchmark inside a Linux container
#
# Only the bench package is compiled (pure stdlib, no internal deps),
# so we only need go.mod/go.sum + the bench/ directory.
FROM golang:1.26.2@sha256:fcdb3e42c5544e9682a635771eac76a698b66de79b1b50ec5b9ce5c5f14ad775

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY bench/ ./bench/

ENTRYPOINT ["go", "run", "./bench"]
