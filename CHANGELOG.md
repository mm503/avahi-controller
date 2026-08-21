# Changelog

## [0.6.1](https://github.com/mm503/avahi-controller/compare/v0.6.0...v0.6.1) (2026-08-21)


### Bug Fixes

* **deps:** update kubernetes monorepo to v0.36.4 ([6c232c5](https://github.com/mm503/avahi-controller/commit/6c232c51d0e654cb541b687caaed525aacb2030f))

## [0.6.0](https://github.com/mm503/avahi-controller/compare/v0.5.2...v0.6.0) (2026-08-20)


### Features

* **ci:** point dev builds at ghcr ([2c6bb50](https://github.com/mm503/avahi-controller/commit/2c6bb5012daff54587ed4852bbdaf2a089740c61))
* **ci:** publish release images to ghcr ([85f6e0e](https://github.com/mm503/avahi-controller/commit/85f6e0e10c88539e9caf21ba217d31ea361f47e6))
* point deployment artifacts at ghcr image ([11e1b44](https://github.com/mm503/avahi-controller/commit/11e1b44c41c19b46809d311db2aa310423d463de))


### Bug Fixes

* **deps:** update go toolchain directive to v1.26.6 ([2a5ec08](https://github.com/mm503/avahi-controller/commit/2a5ec08b52a966ec258a4c1a98e5586bc3b4e6f1))
* **deps:** update go toolchain directive to v1.27.0 ([fabeed2](https://github.com/mm503/avahi-controller/commit/fabeed2767b750a6461d90e23867a65a65e7fd89))
* **deps:** update golang docker tag to v1.26.6 ([83a1af5](https://github.com/mm503/avahi-controller/commit/83a1af5b7a2633a9ae0b561bc6ddb50ff3f11d04))
* **deps:** update golang docker tag to v1.27.0 ([b3ed7f6](https://github.com/mm503/avahi-controller/commit/b3ed7f6bf2bfe96da0718370be4a502859405033))

## [0.5.2](https://github.com/mm503/avahi-controller/compare/v0.5.1...v0.5.2) (2026-08-06)


### Bug Fixes

* **controller:** log service adds at info level ([cbc6184](https://github.com/mm503/avahi-controller/commit/cbc6184721cb21b18b9530b7fcfea12d3581b7dc))

## [0.5.1](https://github.com/mm503/avahi-controller/compare/v0.5.0...v0.5.1) (2026-08-03)


### Bug Fixes

* **deps:** update actions/setup-go action to v7 ([1ee2bea](https://github.com/mm503/avahi-controller/commit/1ee2bea17c2f51243b4925eb40fcb7582970fa06))
* **deps:** update kubernetes monorepo to v0.36.3 ([f025c19](https://github.com/mm503/avahi-controller/commit/f025c193b613c739ff0628a97ecff84844337917))
* **deps:** update module github.com/go-logr/logr to v1.4.4 ([c677987](https://github.com/mm503/avahi-controller/commit/c6779871318e95b4165e47f53658966801cdbdd3))

## [0.5.0](https://github.com/mm503/avahi-controller/compare/v0.4.9...v0.5.0) (2026-07-10)


### Features

* **reconciler:** log existing hosts file entries at startup ([c0f52f0](https://github.com/mm503/avahi-controller/commit/c0f52f0befaf8b7d3ca224eef00c7491bb0a42ff))

## [0.4.9](https://github.com/mm503/avahi-controller/compare/v0.4.8...v0.4.9) (2026-07-08)


### Bug Fixes

* **deps:** update golang docker tag to v1.26.5 ([ecd659a](https://github.com/mm503/avahi-controller/commit/ecd659a5b59cac02e4aa12e7e546a4445a870fdb))

## [0.4.8](https://github.com/mm503/avahi-controller/compare/v0.4.7...v0.4.8) (2026-07-01)


### Bug Fixes

* **avahi:** plumb context through Reload and bound the D-Bus call ([e9a0710](https://github.com/mm503/avahi-controller/commit/e9a0710cb4a006f6224087e79340fcc2f88324d7))
* **controller:** wait for in-flight reconcile before Run returns ([9184c6d](https://github.com/mm503/avahi-controller/commit/9184c6df1dce1d06bc6383e9a0fcc45b0eea4cc1))
* **deps:** update actions/checkout action to v7 ([#43](https://github.com/mm503/avahi-controller/issues/43)) ([21b3acb](https://github.com/mm503/avahi-controller/commit/21b3acbba856bdecbec56650c0ff5a727fc5e363))
* **hostsfile:** rebuild managed block when markers are malformed ([7c10a24](https://github.com/mm503/avahi-controller/commit/7c10a24d7ba78cdeef4922543350e3f3b99b050f))
* **reconciler:** make hostname conflict detection case-insensitive ([aa32d8a](https://github.com/mm503/avahi-controller/commit/aa32d8adc4f7b475202b1f107a1baca66760fc02))
* **reconciler:** reject IP-shaped hostnames, warn once on non-.local names ([1dde1c1](https://github.com/mm503/avahi-controller/commit/1dde1c1bb550e4dfa2b8db1a6266f8dbaee21306))
* **reconciler:** scan all LoadBalancer ingress entries for an IP ([26356f0](https://github.com/mm503/avahi-controller/commit/26356f03207063968f166215e5f4ae26d86ed327))
* **reconciler:** surface services stuck waiting for a LoadBalancer IP ([fb04292](https://github.com/mm503/avahi-controller/commit/fb0429225a733fb0e07381e14f29bb2de0dbc41e))

## [0.4.7](https://github.com/mm503/avahi-controller/compare/v0.4.6...v0.4.7) (2026-06-14)


### Bug Fixes

* **deps:** update kubernetes monorepo to v0.36.2 ([#41](https://github.com/mm503/avahi-controller/issues/41)) ([4d5cbb4](https://github.com/mm503/avahi-controller/commit/4d5cbb4ab2036897a16f35157235656f6058a0a4))

## [0.4.6](https://github.com/mm503/avahi-controller/compare/v0.4.5...v0.4.6) (2026-06-12)


### Bug Fixes

* **controller:** treat SIGTERM during cache sync as clean shutdown ([#35](https://github.com/mm503/avahi-controller/issues/35)) ([f531a44](https://github.com/mm503/avahi-controller/commit/f531a44dbf397dec782f56f0d83a27edbbd1463d))
* make entry ordering and conflict resolution deterministic ([#37](https://github.com/mm503/avahi-controller/issues/37)) ([f94bff9](https://github.com/mm503/avahi-controller/commit/f94bff9f3dcee3706d0fa9bab6fdbea47e4e5e78))
* **reconciler:** retry avahi reload after a failed attempt ([#36](https://github.com/mm503/avahi-controller/issues/36)) ([866d0db](https://github.com/mm503/avahi-controller/commit/866d0db603cdc7686eb5cdb620f7918691247176))
* **reconciler:** validate hostname annotation before writing hosts file ([#38](https://github.com/mm503/avahi-controller/issues/38)) ([095273f](https://github.com/mm503/avahi-controller/commit/095273f904c8cb6f071b14f00cbb6e9f369f0427))

## [0.4.5](https://github.com/mm503/avahi-controller/compare/v0.4.4...v0.4.5) (2026-06-03)


### Bug Fixes

* **deps:** update golang docker tag to v1.26.4 ([#33](https://github.com/mm503/avahi-controller/issues/33)) ([0674685](https://github.com/mm503/avahi-controller/commit/06746855b3835090e8160303963397151a8f8d06))

## [0.4.4](https://github.com/mm503/avahi-controller/compare/v0.4.3...v0.4.4) (2026-05-18)


### Bug Fixes

* **deps:** update kubernetes monorepo to v0.36.1 ([#31](https://github.com/mm503/avahi-controller/issues/31)) ([e686d65](https://github.com/mm503/avahi-controller/commit/e686d65afa06331fb896acb92949271902e42850))

## [0.4.3](https://github.com/mm503/avahi-controller/compare/v0.4.2...v0.4.3) (2026-05-09)


### Bug Fixes

* **ci:** adjust renovate to categorize updates correctly ([ed237c7](https://github.com/mm503/avahi-controller/commit/ed237c71dd10c1aac8df6081196b71b1eb8b9a0f))
* **deps:** update golang docker tag to v1.26.3 ([#30](https://github.com/mm503/avahi-controller/issues/30)) ([79d8d9d](https://github.com/mm503/avahi-controller/commit/79d8d9dc12cccafacc652c7184e317a79c7211e3))

## [0.4.2](https://github.com/mm503/avahi-controller/compare/v0.4.1...v0.4.2) (2026-04-28)


### Bug Fixes

* **deps:** update kubernetes monorepo to v0.36.0 ([#24](https://github.com/mm503/avahi-controller/issues/24)) ([865bf7c](https://github.com/mm503/avahi-controller/commit/865bf7cf1184e3e0986ef1a7795d80df81eb0784))

## [0.4.1](https://github.com/mm503/avahi-controller/compare/v0.4.0...v0.4.1) (2026-04-20)


### Bug Fixes

* **deps:** update kubernetes monorepo to v0.35.4 ([#22](https://github.com/mm503/avahi-controller/issues/22)) ([d4bdcef](https://github.com/mm503/avahi-controller/commit/d4bdcefae618a0ec007cf44782be7671334c9612))

## [0.4.0](https://github.com/mm503/avahi-controller/compare/v0.3.0...v0.4.0) (2026-04-14)


### Features

* **chart:** add README, LICENSE, ArtifactHub annotations and node label notice ([#21](https://github.com/mm503/avahi-controller/issues/21)) ([345b9ec](https://github.com/mm503/avahi-controller/commit/345b9ec2eb844f91814dc2fe25c0b5f5c9fa00c7))


### Bug Fixes

* **ci:** set correct permissions ([#19](https://github.com/mm503/avahi-controller/issues/19)) ([f3ea9fe](https://github.com/mm503/avahi-controller/commit/f3ea9fef24c8eb87b7d82fe12333407ff4ab6016))

## [0.3.0](https://github.com/mm503/avahi-controller/compare/v0.2.0...v0.3.0) (2026-04-13)


### Features

* add helm and update CI ([#14](https://github.com/mm503/avahi-controller/issues/14)) ([3d21b74](https://github.com/mm503/avahi-controller/commit/3d21b74c7a57abc3e15cde4b88a2d766fca21e59))
* **ci:** avoid qemu for multiarch builds ([#12](https://github.com/mm503/avahi-controller/issues/12)) ([fa2d5dc](https://github.com/mm503/avahi-controller/commit/fa2d5dc178e081c7c9a06ba35cb101dab05fef6f))

## [0.2.0](https://github.com/mm503/avahi-controller/compare/v0.1.0...v0.2.0) (2026-04-11)


### Features

* add initial version ([9b37428](https://github.com/mm503/avahi-controller/commit/9b374288a823feafa1e4cd46191924e81bce8884))
* add renovate ([f446388](https://github.com/mm503/avahi-controller/commit/f446388ddef138aa5dd4059f2bb8a255da170928))
