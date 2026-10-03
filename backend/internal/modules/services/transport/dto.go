package transport

import "github.com/MagicalWig34653/turaco/backend/internal/modules/services/application"

type serviceDTO struct {
	ID            string  `json:"id"`
	Reference     string  `json:"reference"`
	Name          string  `json:"name"`
	Description   *string `json:"description"`
	OwnerUserID   *string `json:"ownerUserId"`
	OwnerTeamID   *string `json:"ownerTeamId"`
	SupportTeamID *string `json:"supportTeamId"`
	Criticality   string  `json:"criticality"`
	Status        string  `json:"status"`
	StatusReason  *string `json:"statusReason"`
	RetiredAt     *string `json:"retiredAt"`
	Version       int     `json:"version"`
	CreatedAt     string  `json:"createdAt"`
	UpdatedAt     string  `json:"updatedAt"`
}

func toService(s application.Service) serviceDTO {
	return serviceDTO{ID: s.ID, Reference: s.Reference, Name: s.Name, Description: s.Description, OwnerUserID: s.OwnerUserID,
		OwnerTeamID: s.OwnerTeamID, SupportTeamID: s.SupportTeamID, Criticality: s.Criticality, Status: s.Status, StatusReason: s.StatusReason,
		RetiredAt: tsPtr(s.RetiredAt), Version: s.Version, CreatedAt: ts(s.CreatedAt), UpdatedAt: ts(s.UpdatedAt)}
}

type listDTO struct {
	Items      []serviceDTO `json:"items"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

// nodeDTO leaves out what the caller may not see (name, reference, status, criticality).
type nodeDTO struct {
	Type        string  `json:"type"`
	ID          string  `json:"id"`
	Name        *string `json:"name,omitempty"`
	Reference   *string `json:"reference,omitempty"`
	Status      *string `json:"status,omitempty"`
	Criticality *string `json:"criticality,omitempty"`
	Missing     bool    `json:"missing,omitempty"`
}

func toNode(n application.NodeInfo) nodeDTO {
	return nodeDTO{Type: n.Type, ID: n.ID, Name: n.Name, Reference: n.Reference, Status: n.Status, Criticality: n.Criticality, Missing: n.Missing}
}

type linkDTO struct {
	RelationshipID string  `json:"relationshipId"`
	Type           string  `json:"type"`
	Confidence     string  `json:"confidence"`
	Since          string  `json:"since"`
	Node           nodeDTO `json:"node"`
}

func toLink(l application.Link) linkDTO {
	return linkDTO{RelationshipID: l.RelationshipID, Type: l.Type, Confidence: l.Confidence, Since: ts(l.Since), Node: toNode(l.Node)}
}

type detailDTO struct {
	serviceDTO
	Dependencies          []linkDTO `json:"dependencies"`
	Dependents            []linkDTO `json:"dependents"`
	DependenciesTruncated bool      `json:"dependenciesTruncated"`
	DependentsTruncated   bool      `json:"dependentsTruncated"`
}

type pathEdgeDTO struct {
	RelationshipID string `json:"relationshipId"`
	FromType       string `json:"fromType"`
	FromID         string `json:"fromId"`
	ToType         string `json:"toType"`
	ToID           string `json:"toId"`
	Type           string `json:"type"`
	Confidence     string `json:"confidence"`
}

type impactNodeDTO struct {
	nodeDTO
	Depth      int           `json:"depth"`
	Confidence string        `json:"confidence"`
	Path       []pathEdgeDTO `json:"path"`
}

type impactDTO struct {
	Start        nodeDTO         `json:"start"`
	Direction    string          `json:"direction"`
	MaxDepth     int             `json:"maxDepth"`
	Items        []impactNodeDTO `json:"items"`
	Truncated    bool            `json:"truncated"`
	DepthLimited bool            `json:"depthLimited"`
	NodeLimited  bool            `json:"nodeLimited"`
}

func toImpact(r application.ImpactResult) impactDTO {
	out := impactDTO{Start: toNode(r.Start), Direction: r.Direction, MaxDepth: r.MaxDepth, Items: make([]impactNodeDTO, 0, len(r.Nodes)),
		Truncated: r.Truncated, DepthLimited: r.DepthLimited, NodeLimited: r.NodeLimited}
	for _, n := range r.Nodes {
		d := impactNodeDTO{nodeDTO: toNode(n.NodeInfo), Depth: n.Depth, Confidence: n.Confidence, Path: make([]pathEdgeDTO, 0, len(n.Path))}
		for _, e := range n.Path {
			d.Path = append(d.Path, pathEdgeDTO(e))
		}
		out.Items = append(out.Items, d)
	}
	return out
}
