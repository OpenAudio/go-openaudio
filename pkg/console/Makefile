.PHONY: run tailwind-watch templ-watch go-watch tailwind-install

run:
	@bash -c ' \
		trap "echo Stopping...; kill 0" SIGINT; \
		$(MAKE) tailwind-watch & \
		$(MAKE) templ-watch & \
		$(MAKE) go-watch & \
		wait'

tailwind-watch:
	./tmp/tailwindcss -i assets/input.css -o assets/css/output.css --watch 

templ-watch:
	templ generate --watch

go-watch:
	wgo -file=.css -file=.js -file=.go go run ./cmd/main.go

tailwind-install:
	curl -sLO https://github.com/tailwindlabs/tailwindcss/releases/latest/download/tailwindcss-macos-arm64
	chmod +x tailwindcss-macos-arm64
	mv tailwindcss-macos-arm64 ./tmp/tailwindcss

pg:
	docker run --name audiusd-postgres -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=audiusd -d -p 5444:5432 postgres

node:
	docker run -d \
		--name audiusd-stage \
		-p 3000:80 \
		--pull=always \
		-v $(CURDIR)/tmp/bolt:/data/bolt \
		-e NETWORK=stage \
		-e audius_core_root_dir=/data/bolt \
		-e stateSyncEnable=true \
		-e stateSyncServeSnapshots=true \
		-e stateSyncRPCServers=https://creatornode11.staging.audius.co/core/crpc,https://creatornode11.staging.audius.co/core/crpc \
		audius/audiusd:prerelease
