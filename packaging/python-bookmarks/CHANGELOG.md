# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

### Added

- `input_slider`, a drop-in constructor that preserves bookmarked date and
  datetime values, including ranges and module inputs. Apps must replace their
  affected `ui.input_slider` calls to use the helper. Invalid saved date values
  and mismatched range shapes fall back to the app default.

## [0.5.7](https://github.com/rvben/shinyhub/compare/shinyhub-bookmarks-v0.5.6...shinyhub-bookmarks-v0.5.7) - 2026-11-09

### Added

- `input_selectize`, a drop-in constructor that preserves choices when restoring
  single- and multi-select bookmarks on affected Shiny releases. Apps must
  replace their `ui.input_selectize` calls to use the helper.

## [0.5.6](https://github.com/rvben/shinyhub/compare/shinyhub-bookmarks-v0.5.5...shinyhub-bookmarks-v0.5.6) - 2026-11-04

### Fixed

- Distinguish a disconnected session from unsaved input changes so compatible ShinyHub servers can safely restore the last saved view. Older servers retain manual recovery.

### Added

- **recovery**: preserve deployment and bookmarked session outcomes ([6a579b6](https://github.com/rvben/shinyhub/commit/6a579b672ebedb9bc1ac72d902056df5190e3f5b))

## [0.5.5](https://github.com/rvben/shinyhub/compare/shinyhub-bookmarks-v0.5.4...shinyhub-bookmarks-v0.5.5) - 2026-10-26

### Fixed

- **bookmarks**: keep private inputs and agent payloads out of view URLs ([5ca0c99](https://github.com/rvben/shinyhub/commit/5ca0c99597bda072c05a29d547df59245cb77068))
