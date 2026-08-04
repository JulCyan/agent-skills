package mediasync

import "shopify-media-sync/internal/config"

type Store = config.Store
type StoresConfig = config.StoresConfig

var loadStoresConfig = config.LoadStoresConfig
var loadStoresConfigBytes = config.LoadStoresConfigBytes
var selectStores = config.SelectStores
var findUp = config.FindUp
var findUpFrom = config.FindUpFrom
var splitCSV = config.SplitCSV
var loadDotEnv = config.LoadDotEnv
var readDotEnv = config.ReadDotEnv
