package mediasync

import "shopify-media-sync/internal/runstate"

type DesiredManifest = runstate.DesiredManifest
type DesiredRow = runstate.DesiredRow
type PlanInput = runstate.PlanInput
type Plan = runstate.Plan
type PlanSummary = runstate.PlanSummary
type PlanChange = runstate.PlanChange
type Evidence = runstate.Evidence
type ApplyPreviewBinding = runstate.ApplyPreviewBinding
type EvidenceRecord = runstate.EvidenceRecord
type StageEvidence = runstate.StageEvidence
type TranslationReadback = runstate.TranslationReadback
type ApplyCounts = runstate.ApplyCounts
type ApplyRowError = runstate.ApplyRowError
type ApplySummary = runstate.ApplySummary
type ApplyAttempt = runstate.ApplyAttempt

var LoadEvidence = runstate.LoadEvidence
var mergeEvidenceRecord = runstate.MergeEvidenceRecord
