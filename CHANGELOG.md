# Changelog

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
