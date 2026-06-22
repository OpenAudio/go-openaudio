.PHONY: test
test:
	@if [ -z "$(OPENAUDIO_CI)" ]; then \
		$(MAKE) -C ../.. docker-dev; \
	fi
	go test -v -tags integration -run TestGenesisWriter -timeout 30m ./...
