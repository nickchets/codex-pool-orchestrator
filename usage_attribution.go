package main

import "strings"

// UsageAttribution captures non-secret request/key metadata to attach to usage rows.
// It intentionally stores virtual key IDs/names only; raw API key material must never
// be copied into this struct.
type UsageAttribution struct {
	TokenID          string
	TokenName        string
	CredentialKind   CredentialKind
	ClientEndpoint   string
	Stream           bool
	Status           int
	Estimated        bool
	ErrorClass       string
	ContinuationUsed bool
	SegmentCount     int
}

func buildUsageAttribution(admission AdmissionResult, clientEndpoint string, stream bool) UsageAttribution {
	attr := UsageAttribution{
		ClientEndpoint: strings.TrimSpace(clientEndpoint),
		Stream:         stream,
	}
	if strings.TrimSpace(admission.TokenID) == "" {
		return attr
	}
	attr.TokenID = strings.TrimSpace(admission.TokenID)
	attr.TokenName = strings.TrimSpace(admission.TokenName)
	attr.CredentialKind = admission.CredentialKind
	return attr
}

func applyUsageAttribution(ru *RequestUsage, attr UsageAttribution) *RequestUsage {
	if ru == nil {
		return nil
	}
	if attr.TokenID != "" {
		ru.TokenID = attr.TokenID
		ru.TokenName = attr.TokenName
		ru.CredentialKind = attr.CredentialKind
	}
	if attr.ClientEndpoint != "" {
		ru.ClientEndpoint = attr.ClientEndpoint
	}
	ru.Stream = attr.Stream
	if attr.Status != 0 {
		ru.Status = attr.Status
	}
	if attr.Estimated {
		ru.Estimated = true
	}
	if attr.ErrorClass != "" {
		ru.ErrorClass = attr.ErrorClass
	}
	if attr.ContinuationUsed {
		ru.ContinuationUsed = true
	}
	if attr.SegmentCount > 0 {
		ru.SegmentCount = attr.SegmentCount
	}
	return ru
}
