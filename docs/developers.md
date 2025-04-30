# Development

> Assumes development is done on macOS with apple silicon.

**Prerequisites:**

1. Clone the repo

```bash
git clone git@github.com:AudiusProject/audiusd.git
cd audiusd
```

2. Add the local dev x509 cert to your keychain so you will have green ssl in your browser.

```bash
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain dev/tls/cert.pem
```

3. Add the devnet hosts to your `/etc/hosts` file:

```bash
echo "127.0.0.1       node1.audiusd.devnet node2.audiusd.devnet node3.audiusd.devnet node4.audiusd.devnet" | sudo tee -a /etc/hosts
```

## Build and Run

Build and run a local devnet with 4 nodes.

```bash
make up
```

Access the dev nodes.

```bash
# add -k if you don't have the cert in your keychain
curl https://node1.audiusd.devnet/health-check
curl https://node2.audiusd.devnet/health-check
curl https://node3.audiusd.devnet/health-check
curl https://node4.audiusd.devnet/health-check

# view in browser (quit and re-open if you added the cert and still get browser warnings)
open https://node1.audiusd.devnet/console
open https://node2.audiusd.devnet/console
open https://node3.audiusd.devnet/console
open https://node4.audiusd.devnet/console
```

Smoke test...

```bash
# after 5-10s there should be 4 nodes registered
# this validates that the registry bridge is working,
# as only nodes 1 and 2 are defined in the genesis file as validators

$ curl -s https://node1.audiusd.devnet/core/nodes | jq .
{
  "data": [
    "https://node2.audiusd.devnet",
    "https://node1.audiusd.devnet",
    "https://node3.audiusd.devnet",
    "https://node4.audiusd.devnet"
  ]
}

# also...
open https://node1.audiusd.devnet/console/nodes
```

Inspect Proof of Useful Work.
```bash
open https://node1.audiusd.devnet/console/uptime
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
docker run --rm -it -p 80:80 -p 443:443 -e NETWORK=prod audius/audiusd:dev

# peer with stage with hot reloading
docker run --rm -it \
  -p 80:80 \
  -p 443:443 \
  -e NETWORK="stage" \
  -v $(pwd)/cmd:/app/cmd \
  -v $(pwd)/pkg:/app/pkg \
  audius/audiusd:dev
```

## Run tests

```bash
# "unit" tests
make mediorum-test

# "integration" tests
make core-test
```
