# Changelog

## [1.4.0](https://github.com/OpenAudio/go-openaudio/compare/v1.3.0...v1.4.0) (2026-05-20)


### Features

* **etl:** allowed_api_keys + access_authorities normalization (parity 5B) ([#250](https://github.com/OpenAudio/go-openaudio/issues/250)) ([247007a](https://github.com/OpenAudio/go-openaudio/commit/247007a881984cff9a29320a66ac39863225b0a5))
* **etl:** decode hashid-encoded track_ids in playlist_contents (parity 5A) ([#269](https://github.com/OpenAudio/go-openaudio/issues/269)) ([c8fddb6](https://github.com/OpenAudio/go-openaudio/commit/c8fddb6fb9a51db7baf7fa5f00b8e7adb79e88dd))
* **etl:** index oauth_redirect_uris on developer app create/update (parity 5E) ([#252](https://github.com/OpenAudio/go-openaudio/issues/252)) ([bc86dea](https://github.com/OpenAudio/go-openaudio/commit/bc86deab948b20673c3bfa315a469592344f0d68))
* **mediorum:** flip health-check unhealthy when bucket writes fail ([#297](https://github.com/OpenAudio/go-openaudio/issues/297)) ([94793f9](https://github.com/OpenAudio/go-openaudio/commit/94793f91594f81f80421e3e00668552e8c1f9af8))


### Bug Fixes

* **ci:** wait for SHA-tagged image before retagging release tag ([#291](https://github.com/OpenAudio/go-openaudio/issues/291)) ([cc875ef](https://github.com/OpenAudio/go-openaudio/commit/cc875ef5459d6cca28a112cfc2694f6b6fe6ee88))
* **core:** pass --disable-triggers to pg_restore data section ([#293](https://github.com/OpenAudio/go-openaudio/issues/293)) ([438c29f](https://github.com/OpenAudio/go-openaudio/commit/438c29f52683d823f751a003360b2439e86b6c06))
* **etl:** comment threading guards (parity 5C) ([#251](https://github.com/OpenAudio/go-openaudio/issues/251)) ([56db895](https://github.com/OpenAudio/go-openaudio/commit/56db89571b49d79008885fc01cb81daac7c8f3ce))
* **etl:** normalize empty playlist_contents on update (apps[#14306](https://github.com/OpenAudio/go-openaudio/issues/14306) parity) ([#265](https://github.com/OpenAudio/go-openaudio/issues/265)) ([a8016e9](https://github.com/OpenAudio/go-openaudio/commit/a8016e9e1661ec5344d3d0d29a1346f7dd190c69))

## [1.3.0](https://github.com/OpenAudio/go-openaudio/compare/v1.2.14...v1.3.0) (2026-05-19)


### Features

* **console:** surface archive storage stats with per-bucket disk indicators ([#282](https://github.com/OpenAudio/go-openaudio/issues/282)) ([1eb8a38](https://github.com/OpenAudio/go-openaudio/commit/1eb8a389f5c343831b08c28aa9379b8c2cc05f7d))
* **core/console:** Storage tab polish (truncate long tables, Files Changed col) ([#279](https://github.com/OpenAudio/go-openaudio/issues/279)) ([356ebfc](https://github.com/OpenAudio/go-openaudio/commit/356ebfc428a0a42e3a497c0b0331aa1f94b5b609))
* **etl:** enforce immutable field set on track/playlist update (parity 2D) ([#245](https://github.com/OpenAudio/go-openaudio/issues/245)) ([6a2be4b](https://github.com/OpenAudio/go-openaudio/commit/6a2be4bd82c396483e42483b9197d591aad7b78e))
* **etl:** scheduled release publisher (parity 4A) ([#247](https://github.com/OpenAudio/go-openaudio/issues/247)) ([e8adcb9](https://github.com/OpenAudio/go-openaudio/commit/e8adcb92d19f060c4128b07722781d05455b0ede))
* **etl:** track_price_history + album_price_history tables and writes ([#243](https://github.com/OpenAudio/go-openaudio/issues/243)) ([52b771a](https://github.com/OpenAudio/go-openaudio/commit/52b771a49c63907d8f7d784bf927a63809b9a80b))
* **mediorum:** optional parallelism for repair runs ([#281](https://github.com/OpenAudio/go-openaudio/issues/281)) ([a23df44](https://github.com/OpenAudio/go-openaudio/commit/a23df44d8b1ab5a527163f722b56f80271fcfa37))
* **mediorum:** read-repair on failed PoS challenge ([#280](https://github.com/OpenAudio/go-openaudio/issues/280)) ([c9e8926](https://github.com/OpenAudio/go-openaudio/commit/c9e8926bc5ee7d33d3d0e5b16c2b1752ed2ce552))


### Bug Fixes

* **ci:** rename release-please config to expected filename ([#286](https://github.com/OpenAudio/go-openaudio/issues/286)) ([c7dc98b](https://github.com/OpenAudio/go-openaudio/commit/c7dc98b4341236fd2ed3f0f19d6014f31d212768))
* **ci:** use App client-id (not app-id) for release-please auth ([#290](https://github.com/OpenAudio/go-openaudio/issues/290)) ([4d2e6aa](https://github.com/OpenAudio/go-openaudio/commit/4d2e6aa037f3adc3e2b95c33f76950327a86f28f))
* **etl:** restore 0017 stub so golang-migrate can step past version 17 ([#283](https://github.com/OpenAudio/go-openaudio/issues/283)) ([17efa20](https://github.com/OpenAudio/go-openaudio/commit/17efa201098353a35e2f49b7be6e8bf0dc381cac))
* **mediorum:** suppress no-op uploads ops on replication retries ([#278](https://github.com/OpenAudio/go-openaudio/issues/278)) ([2a4c77a](https://github.com/OpenAudio/go-openaudio/commit/2a4c77a4d4ca9d5a0ff8faff0d966ed1f7f5fc62))
* trigger initial release-please dry-run ([#285](https://github.com/OpenAudio/go-openaudio/issues/285)) ([0841e17](https://github.com/OpenAudio/go-openaudio/commit/0841e173300a6e15caee74e78fde01e6d20b2b38))
