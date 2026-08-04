package mediasync

import (
	"net/http"

	"shopify-media-sync/internal/shopifyadmin"
)

type ShopifyClient = shopifyadmin.ShopifyClient
type ShopifyUserError = shopifyadmin.ShopifyUserError
type StagedTarget = shopifyadmin.StagedTarget
type ShopifyFileNode = shopifyadmin.ShopifyFileNode
type VideoSource = shopifyadmin.VideoSource
type ShopLocale = shopifyadmin.ShopLocale
type mutationAttemptError = shopifyadmin.MutationAttemptError
type ShopifyClientOption = shopifyadmin.ClientOption

const shopifyAPIVersion = shopifyadmin.APIVersion

var NewShopifyClient = shopifyadmin.NewShopifyClient
var withShopifyEndpoints = shopifyadmin.WithEndpoints
var withShopifyBackoff = shopifyadmin.WithBackoff
var newShopifyClient = func(client *http.Client) *ShopifyClient {
	return shopifyadmin.NewShopifyClient(client)
}
var errShopifyFileNotFound = shopifyadmin.ErrShopifyFileNotFound
var envSuffix = shopifyadmin.EnvSuffix
var shopifyAPIVersionForStore = shopifyadmin.APIVersionForStore
var isVideoNode = shopifyadmin.IsVideoNode
var videoSourceURL = shopifyadmin.VideoSourceURL
var videoNodeReady = shopifyadmin.VideoNodeReady
var fileNodeReadyWithImageMetadata = shopifyadmin.FileNodeReadyWithImageMetadata
var shopifyFilenameSearchQuery = shopifyadmin.ShopifyFilenameSearchQuery
var shopifyFileNodeFilename = shopifyadmin.ShopifyFileNodeFilename
var validateTranslationReadback = shopifyadmin.ValidateTranslationReadback
var filterRegisterableTranslations = shopifyadmin.FilterRegisterableTranslations
var newMutationAttemptError = shopifyadmin.NewMutationAttemptError
var wrapMutationAttemptError = shopifyadmin.WrapMutationAttemptError
