package public

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai"
)

const (
	aiMaxSoftware = 50
	aiMaxFindings = 20
	aiTime        = "2006-01-02T15:04:05Z07:00"
)

// deviceContext is the dedicated egress DTO of devices.context_summary (F12 A13). It deliberately has no serial
// number, provider ids, Asset id, logon names, remote-access identifiers, session data or configuration values.
// Everything in it is provider-observed data and says so.
type deviceContext struct {
	Name              string            `json:"name"`
	OSPlatform        string            `json:"osPlatform"`
	OSVersion         *string           `json:"osVersion"`
	Manufacturer      *string           `json:"manufacturer"`
	Model             *string           `json:"model"`
	Ownership         string            `json:"ownership"`
	ComplianceState   string            `json:"complianceState"`
	LastCheckinAt     *string           `json:"lastCheckinAt"`
	Retired           bool              `json:"retired"`
	LinkedToAsset     bool              `json:"linkedToAsset"`
	DataOrigin        string            `json:"dataOrigin"`
	ObservedSource    string            `json:"observedSource"`
	ObservedAt        string            `json:"observedAt"`
	LastSyncedAt      string            `json:"lastSyncedAt"`
	SoftwareCount     int               `json:"softwareCount"`
	Software          []deviceSoftware  `json:"software"`
	OpenFindingsCount int               `json:"openFindingsCount"`
	Findings          []deviceFindingAI `json:"findings"`
}

type deviceSoftware struct {
	Name      string  `json:"name"`
	Version   string  `json:"version"`
	Publisher *string `json:"publisher"`
}

type deviceFindingAI struct {
	Kind     string `json:"kind"`
	RaisedAt string `json:"raisedAt"`
}

// AITools returns the AI Tools of the Endpoints module.
func AITools(svc *application.Service) []ai.Tool {
	return []ai.Tool{{
		Name:        "devices.context_summary",
		Description: "Read a summary of one managed Device the user may see: operating system, ownership, compliance state as reported by the management provider, the last check-in, installed software and open data-quality findings. All values are provider-observed and carry their source and freshness.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["deviceId"],"properties":{"deviceId":{"type":"string","format":"uuid","maxLength":36,"x-audit":"id","description":"The Device id."}}}`),
		Permission:  "endpoints.view",
		Risk:        ai.RiskRead,
		Target:      &ai.Target{Type: "device", Arg: "deviceId"},
		Output: []ai.Field{
			{Path: "name", Class: ai.ClassDeviceContext, MaxLen: 200},
			{Path: "osPlatform", Class: ai.ClassDeviceContext, MaxLen: 60},
			{Path: "osVersion", Class: ai.ClassDeviceContext, MaxLen: 100},
			{Path: "manufacturer", Class: ai.ClassDeviceContext, MaxLen: 100},
			{Path: "model", Class: ai.ClassDeviceContext, MaxLen: 100},
			{Path: "ownership", Class: ai.ClassDeviceContext, MaxLen: 20},
			{Path: "complianceState", Class: ai.ClassDeviceContext, MaxLen: 40},
			{Path: "lastCheckinAt", Class: ai.ClassDeviceContext, MaxLen: 40},
			{Path: "retired", Class: ai.ClassDeviceContext},
			{Path: "linkedToAsset", Class: ai.ClassDeviceContext},
			{Path: "dataOrigin", Class: ai.ClassDeviceContext, MaxLen: 40},
			{Path: "observedSource", Class: ai.ClassDeviceContext, MaxLen: 40},
			{Path: "observedAt", Class: ai.ClassDeviceContext, MaxLen: 40},
			{Path: "lastSyncedAt", Class: ai.ClassDeviceContext, MaxLen: 40},
			{Path: "softwareCount", Class: ai.ClassDeviceContext},
			{Path: "software[].name", Class: ai.ClassDeviceContext, MaxLen: 200},
			{Path: "software[].version", Class: ai.ClassDeviceContext, MaxLen: 100},
			{Path: "software[].publisher", Class: ai.ClassDeviceContext, MaxLen: 200},
			{Path: "openFindingsCount", Class: ai.ClassDeviceContext},
			{Path: "findings[].kind", Class: ai.ClassDeviceContext, MaxLen: 60},
			{Path: "findings[].raisedAt", Class: ai.ClassDeviceContext, MaxLen: 40},
		},
		Handler: func(ctx context.Context, c ai.Caller, input json.RawMessage) (any, error) {
			var in struct {
				DeviceID string `json:"deviceId"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return nil, ai.ErrToolInvalid
			}
			p := application.Principal{UserID: c.UserID, View: c.Has("endpoints.view"), Manage: false, ManagementView: c.Has("endpoint.management.view")}
			d, err := svc.GetDevice(ctx, p, in.DeviceID)
			switch {
			case errors.Is(err, application.ErrNotFound):
				return nil, ai.ErrToolNotFound
			case errors.Is(err, application.ErrForbidden):
				return nil, ai.ErrToolForbidden
			case err != nil:
				return nil, err
			}
			dev := d.Device
			out := deviceContext{Name: dev.Name, OSPlatform: dev.OSPlatform, OSVersion: dev.OSVersion, Manufacturer: dev.Manufacturer, Model: dev.Model,
				Ownership: dev.Ownership, ComplianceState: dev.ComplianceState, Retired: dev.DeletedObservedAt != nil, LinkedToAsset: dev.AssetID != nil,
				DataOrigin: "provider_observed", ObservedSource: dev.Source, ObservedAt: dev.ObservedAt.UTC().Format(aiTime),
				LastSyncedAt: dev.LastSyncedAt.UTC().Format(aiTime), SoftwareCount: len(d.Software), OpenFindingsCount: len(d.Findings),
				Software: []deviceSoftware{}, Findings: []deviceFindingAI{}}
			if dev.LastCheckinAt != nil {
				s := dev.LastCheckinAt.UTC().Format(aiTime)
				out.LastCheckinAt = &s
			}
			for i, sw := range d.Software {
				if i == aiMaxSoftware {
					break
				}
				out.Software = append(out.Software, deviceSoftware{Name: sw.RawName, Version: sw.RawVersion, Publisher: sw.RawPublisher})
			}
			for i, f := range d.Findings {
				if i == aiMaxFindings {
					break
				}
				out.Findings = append(out.Findings, deviceFindingAI{Kind: f.Kind, RaisedAt: f.RaisedAt.UTC().Format(aiTime)})
			}
			return out, nil
		},
	}}
}
