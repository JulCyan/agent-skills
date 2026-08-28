package engine

import _ "embed"

//go:embed npm/package.json
var npmPackageJSON []byte

//go:embed npm/package-lock.json
var npmPackageLock []byte
