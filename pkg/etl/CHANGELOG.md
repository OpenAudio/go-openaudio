# Changelog

## [1.4.0](https://github.com/OpenAudio/go-openaudio/compare/etl/v1.3.0...etl/v1.4.0) (2026-05-20)


### Features

* **etl:** add aggregate and milestone tables ([#238](https://github.com/OpenAudio/go-openaudio/issues/238)) ([18a4c1b](https://github.com/OpenAudio/go-openaudio/commit/18a4c1b42cda8c7b1705512ce96ba8b9760e377a))
* **etl:** allowed_api_keys + access_authorities normalization (parity 5B) ([#250](https://github.com/OpenAudio/go-openaudio/issues/250)) ([247007a](https://github.com/OpenAudio/go-openaudio/commit/247007a881984cff9a29320a66ac39863225b0a5))
* **etl:** decode hashid-encoded track_ids in playlist_contents (parity 5A) ([#269](https://github.com/OpenAudio/go-openaudio/issues/269)) ([c8fddb6](https://github.com/OpenAudio/go-openaudio/commit/c8fddb6fb9a51db7baf7fa5f00b8e7adb79e88dd))
* **etl:** enforce immutable field set on track/playlist update (parity 2D) ([#245](https://github.com/OpenAudio/go-openaudio/issues/245)) ([6a2be4b](https://github.com/OpenAudio/go-openaudio/commit/6a2be4bd82c396483e42483b9197d591aad7b78e))
* **etl:** index oauth_redirect_uris on developer app create/update (parity 5E) ([#252](https://github.com/OpenAudio/go-openaudio/issues/252)) ([bc86dea](https://github.com/OpenAudio/go-openaudio/commit/bc86deab948b20673c3bfa315a469592344f0d68))
* **etl:** index stems, remixes, route_id, and migration playlist routes ([#237](https://github.com/OpenAudio/go-openaudio/issues/237)) ([5432c43](https://github.com/OpenAudio/go-openaudio/commit/5432c43edf2f4f66311f9d887c475d0d97560c12))
* **etl:** playlist_tracks junction table + populate from handlers ([#268](https://github.com/OpenAudio/go-openaudio/issues/268)) ([48e4483](https://github.com/OpenAudio/go-openaudio/commit/48e4483bd21b9dc5b8e7571e4d24516c1cc73bac))
* **etl:** scheduled release publisher (parity 4A) ([#247](https://github.com/OpenAudio/go-openaudio/issues/247)) ([e8adcb9](https://github.com/OpenAudio/go-openaudio/commit/e8adcb92d19f060c4128b07722781d05455b0ede))
* **etl:** track_price_history + album_price_history tables and writes ([#243](https://github.com/OpenAudio/go-openaudio/issues/243)) ([52b771a](https://github.com/OpenAudio/go-openaudio/commit/52b771a49c63907d8f7d784bf927a63809b9a80b))


### Bug Fixes

* **etl:** comment threading guards (parity 5C) ([#251](https://github.com/OpenAudio/go-openaudio/issues/251)) ([56db895](https://github.com/OpenAudio/go-openaudio/commit/56db89571b49d79008885fc01cb81daac7c8f3ce))
* **etl:** normalize empty playlist_contents on update (apps[#14306](https://github.com/OpenAudio/go-openaudio/issues/14306) parity) ([#265](https://github.com/OpenAudio/go-openaudio/issues/265)) ([a8016e9](https://github.com/OpenAudio/go-openaudio/commit/a8016e9e1661ec5344d3d0d29a1346f7dd190c69))
* **etl:** restore 0017 stub so golang-migrate can step past version 17 ([#283](https://github.com/OpenAudio/go-openaudio/issues/283)) ([17efa20](https://github.com/OpenAudio/go-openaudio/commit/17efa201098353a35e2f49b7be6e8bf0dc381cac))
* trigger initial release-please dry-run ([#285](https://github.com/OpenAudio/go-openaudio/issues/285)) ([0841e17](https://github.com/OpenAudio/go-openaudio/commit/0841e173300a6e15caee74e78fde01e6d20b2b38))


### Reverts

* **etl:** drop aggregate + milestone tables (revert [#238](https://github.com/OpenAudio/go-openaudio/issues/238)) ([#267](https://github.com/OpenAudio/go-openaudio/issues/267)) ([4793a7d](https://github.com/OpenAudio/go-openaudio/commit/4793a7dd932e48ceeb6ed922273db69f39c7c65a))
