# Open Audio Protocol

[![license](https://img.shields.io/github/license/OpenAudio/go-openaudio?style=for-the-badge)](https://github.com/OpenAudio/go-openaudio/blob/main/LICENSE) [![Docs](https://img.shields.io/badge/docs-openaudio.org-lightgrey?style=for-the-badge)](https://docs.openaudio.org) [![releases](https://img.shields.io/github/v/release/OpenAudio/go-openaudio?style=for-the-badge)](https://github.com/OpenAudio/go-openaudio/releases/latest) [![Dockerhub](https://img.shields.io/docker/v/openaudio/go-openaudio?sort=semver&style=for-the-badge&label=Docker)](https://hub.docker.com/r/openaudio/go-openaudio)

> A golang implementation of the Open Audio Protocol.

## Quickstart

```bash
docker run --rm -it \
  -p 80:80 \
  -p 443:443 \
  -p 26656:26656 \
  -e OPENAUDIO_TLS_SELF_SIGNED=true \
  -e OPENAUDIO_STORAGE_ENABLED=false \
  openaudio/go-openaudio:stable

# in another terminal session
open https://localhost/console/overview
```

To run and stake a validator and secure the network, visit [docs.openaudio.org](https://docs.openaudio.org/tutorials/run-a-node).

## Local Development

### Prerequisites

Ensure the following are installed:

- Docker
- Docker Compose
- Go v1.25

The remaining dependencies can then be automatically installed with `make`:

```bash
make install-deps
```

### Running local devnet

You can simulate an openaudio network by running multiple nodes on your machine. This makes developing certain features fast and easy.

#### Setup

Add the following hosts to your `/etc/hosts` file:

```bash
echo "127.0.0.1       node1.oap.devnet node2.oap.devnet node3.oap.devnet node4.oap.devnet" | sudo tee -a /etc/hosts
```

Then add the local dev x509 cert to your keychain so you will have green ssl in your browser.

```bash
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain dev/tls/cert.pem
```

#### Run

Build and run a local devnet with 4 nodes.

```bash
make up
```

**Access the dev nodes**

```bash
# add -k if you don't have the cert in your keychain
curl https://node1.oap.devnet/health-check
curl https://node2.oap.devnet/health-check
curl https://node3.oap.devnet/health-check
curl https://node4.oap.devnet/health-check

# view in browser (quit and re-open if you added the cert and still get browser warnings)
open https://node1.oap.devnet/console
open https://node2.oap.devnet/console
open https://node3.oap.devnet/console
open https://node4.oap.devnet/console
```

**Smoke test**

```bash
# after 5-10s there should be 4 nodes registered
# this validates that the registry bridge is working,
# as only nodes 1 and 2 are defined in the genesis file as validators

$ curl -s https://node1.oap.devnet/core/nodes | jq .
{
  "data": [
    "https://node2.oap.devnet",
    "https://node1.oap.devnet",
    "https://node3.oap.devnet",
    "https://node4.oap.devnet"
  ]
}

# or in the UI
open https://node1.oap.devnet/console/nodes

# view uptime across the network
open https://node1.oap.devnet/console/uptime
```

> Note:
> By default, hot reloading is only enabled on node1.oap.devnet to conserve system resources.
> To enable on other nodes, update the corresponding env file in [dev/env](../dev/env).

### Develop against stage or prod

Build a local docker image

```bash
make docker-dev
```

Peer with mainnet

```bash
docker run --rm -it -p 80:80 -p 443:443 -e NETWORK=prod openaudio/go-openaudio:dev
```

### Run tests

Run all tests

```bash
make test
```

Run only storage service tests

```bash
make test-mediorum
```

Run only unittests

```bash
make test-unit
```

Run only integration tests

```bash
make test-integration
```

### ETL

The ETL service indexes blockchain data into the postgres database, enabling faster queries for certain views.

```bash
OPENAUDIO_ETL_ENABLED=true
```

### Explorer

The Explorer provides a web-based interface to browse blocks, transactions, validators, and other data. If enabled, the explorer runs at the site root, e.g. https://node1.oap.devnet/. Explorer requires ETL.

```bash
OPENAUDIO_ETL_ENABLED=true
OPENAUDIO_EXPLORER_ENABLED=true

# View explorer in browser
open https://node1.oap.devnet/
```
