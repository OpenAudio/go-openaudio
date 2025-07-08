FROM debian:bookworm-slim AS prod

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    ca-certificates \
    ffmpeg \
    libfftw3-single3 \
    libfftw3-double3 \
    libsndfile1 \
    libaubio5 \
    libopus0 \
    libvorbis0a \
    libogg0 \
    libflac12 \
    postgresql-15 && \
    rm -rf /var/lib/apt/lists/* && \
    update-ca-certificates

RUN mkdir -p /data/postgres && \
    chown -R postgres:postgres /data/postgres && \
    chmod -R 700 /data/postgres

RUN localedef -i en_US -f UTF-8 en_US.UTF-8

COPY --from=audius/cpp:bookworm /bin/analyze-key /bin/
COPY --from=audius/cpp:bookworm /bin/analyze-bpm /bin/
COPY --from=audius/cpp:bookworm /usr/local/lib/libkeyfinder.so* /usr/local/lib/
RUN ldconfig

RUN mkdir -p /env
COPY ./cmd/audiusd/env/* /env/

COPY ./cmd/audiusd/entrypoint.sh /bin/entrypoint.sh
RUN chmod +x /bin/entrypoint.sh

ARG GIT_SHA
ENV GIT_SHA=$GIT_SHA

EXPOSE 80
EXPOSE 443
EXPOSE 26656

VOLUME ["/data"]

ARG PREBUILT_BINARY
COPY ${PREBUILT_BINARY} /bin/audiusd
RUN chmod +x /bin/audiusd

ENTRYPOINT ["/bin/entrypoint.sh"]


# Image on which to base anything used for development, testing, etc.
FROM prod AS toolbase 

COPY ./dev/tls/cert.pem /usr/share/ca-certificates/devnet.crt
RUN chmod 644 /usr/share/ca-certificates/devnet.crt && \
    echo "devnet.crt" >> /etc/ca-certificates.conf && \
    update-ca-certificates && \
    apt-get update && apt-get install -y curl


# Image from which go tests can be executed in the same network
# with all the audiusd deps installed
FROM toolbase AS harness

COPY --from=golang:alpine /usr/local/go/ /usr/local/go/

ENV GOPATH="/go"
ENV GOROOT="/usr/local/go"
ENV PATH="/usr/local/go/bin:${PATH}"

COPY ./cmd/audiusd/.initdb /app/audiusd/.initdb

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

ENV AUDIUSD_TEST_HARNESS_MODE="true"


# Image for running audiusd during CI tests.
# Ideally nothing should be added here unless strictly necessary.
FROM toolbase AS ci
# no-op


# Image for running audiusd for development.
# Add any developer convenience tools or state here that don't belong in CI
FROM toolbase AS dev
# no-op
