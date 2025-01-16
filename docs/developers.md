# Development

## Local Development

**PREREQUISITES:**

1. Add the local x509 cert to your keychain so you can have green ssl in your browser (You can skip this, but you will get browser warnings).

```bash
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain dev/tls/cert.pem
```

2. Add the devnet hosts to your `/etc/hosts` file:

```bash
echo "127.0.0.1       node1.audiusd.devnet node2.audiusd.devnet node3.audiusd.devnet node4.audiusd.devnet" | sudo tee -a /etc/hosts
```

**BULD AND RUN**

Build and run a local devnet with 4 nodes.

```bash
make audiusd-dev
```

Access the dev nodes:

```bash
# add -k if you don't have the cert in your keychain
curl https://node1.audiusd.devnet/health-check
curl https://node2.audiusd.devnet/health-check
curl https://node3.audiusd.devnet/health-check
curl https://node4.audiusd.devnet/health-check

# view in browser (quit and re open if you get a cert error)
open https://node1.audiusd.devnet/console
open https://node2.audiusd.devnet/console
open https://node3.audiusd.devnet/console
open https://node4.audiusd.devnet/console
```

**CLEANUP**

```bash
make audiusd-dev-down
```

**HOT RELOADING**

Per the mounts in `compose/docker-compose.yml`, hot reloading is enabled on `node1.devnet.audiusd`.
Changes to code in `./cmd/` and `./pkg/` will be reflected after a quick rebuild handled by `air`.

### Dev against stage or prod

```bash
# build a local node
make build-audiusd-dev

# peer with stage
docker run --rm -it -p 80:80 -p 443:443 -e NETWORK=stage audius/audiusd:dev

# peer with prod
docker run --rm -it -p 80:80 -p 443:443 -e NETWORK=prod audius/audiusd:dev
```

## Run tests

```bash
make build-audiusd-test
make mediorum-test
make core-test
```
