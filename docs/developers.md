# Development

> Assumes development is done on macOS with apple silicon.

**Prerequisites:**

1. Clone the repo

```bash
git clone git@github.com:OpenAudio/go-openaudio.git
cd go-openaudio
```

2. Add the devnet hosts to your `/etc/hosts` file:

```bash
echo "127.0.0.1       node1.oap.devnet node2.oap.devnet node3.oap.devnet node4.oap.devnet" | sudo tee -a /etc/hosts
```

3. Add the local dev x509 cert to your keychain so you will have green ssl in your browser.

```bash
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain dev/tls/cert.pem
```

## Build and Run

Build and run a local devnet with 4 nodes.

```bash
make up
```

Access the dev nodes.

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

Smoke test...

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

# also...
open https://node1.oap.devnet/console/nodes
```

Inspect Proof of Useful Work.
```bash
open https://node1.oap.devnet/console/uptime
```

Cleanup.

```bash
make down
```

### Develop against stage or prod

```bash
# build a local node
make docker-dev

# peer with prod
docker run --rm -it -p 80:80 -p 443:443 -e NETWORK=prod audius/openaudio:dev

# peer with stage with hot reloading
docker run --rm -it \
  -p 80:80 \
  -p 443:443 \
  -e NETWORK="stage" \
  -v $(pwd)/cmd:/app/cmd \
  -v $(pwd)/pkg:/app/pkg \
  audius/openaudio:dev
```

## Run tests

```bash
# "unit" tests
make mediorum-test

# "integration" tests
make core-test
```
