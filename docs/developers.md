# Development

```bash
# build a local node
make build-audiusd-local

# sync a locally built node to stage
docker run --rm -it -p 80:80 -p 443:443 -e NETWORK=stage audius/audiusd:local

# sync a locally built node to prod
docker run --rm -it -p 80:80 -p 443:443 audius/audiusd:local

# health check
curl http://localhost/health-check
curl -k https://localhost/health-check

# view in browser
open http://localhost/console/overview
open https://localhost/console/overview
```
