# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

### Fixed

- Distinguish a disconnected session from unsaved input changes so compatible ShinyHub servers can safely restore the last saved view. Older servers retain manual recovery.

## [0.5.5](https://github.com/rvben/shinyhub/compare/shinyhub-bookmarks-v0.5.4...shinyhub-bookmarks-v0.5.5) - 2026-10-26

### Fixed

- **bookmarks**: keep private inputs and agent payloads out of view URLs ([5ca0c99](https://github.com/rvben/shinyhub/commit/5ca0c99597bda072c05a29d547df59245cb77068))
