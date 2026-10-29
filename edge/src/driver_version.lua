-- Driver version. `config.yml` has no standard `version` field, so this is the
-- single source of truth: it goes into the HTTP User-Agent
-- ("smartthings-ps5-control-edge/<version>") and the `edge-vX.Y.Z` release
-- tag must match it (.github/workflows/edge.yml checks).
return "0.1.1"
