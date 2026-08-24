package chrome

import (
	"context"
	"fmt"

	cdproto "github.com/chromedp/cdproto"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// DiagnoseClick identifies the node receiving the target's center click.
func (e *Engine) DiagnoseClick(ctx context.Context, page engine.Page, target engine.InteractionTarget) (engine.ClickDiagnostic, error) {
	x, y, err := e.center(ctx, page, target)
	if err != nil {
		return engine.ClickDiagnostic{}, err
	}
	var located struct {
		NodeID        int64 `json:"nodeId"`
		BackendNodeID int64 `json:"backendNodeId"`
	}
	params := struct {
		X int64 `json:"x"`
		Y int64 `json:"y"`
	}{X: int64(x), Y: int64(y)}
	if err := e.call(ctx, page.SessionID, cdproto.CommandDOMGetNodeForLocation, params, &located); err != nil {
		return engine.ClickDiagnostic{}, fmt.Errorf("resolve click receiver: %w", err)
	}
	diagnostic := engine.ClickDiagnostic{
		Target:   engine.InteractionTarget{NodeID: fmt.Sprint(located.NodeID), BackendNodeID: located.BackendNodeID},
		Targeted: sameTarget(target, located.NodeID, located.BackendNodeID),
	}
	if diagnostic.Targeted {
		return diagnostic, nil
	}
	var described struct {
		Node struct {
			NodeName   string   `json:"nodeName"`
			Attributes []string `json:"attributes"`
		} `json:"node"`
	}
	if located.NodeID != 0 {
		if err := e.call(ctx, page.SessionID, cdproto.CommandDOMDescribeNode, struct {
			NodeID int64 `json:"nodeId"`
		}{located.NodeID}, &described); err == nil {
			diagnostic.Role = described.Node.NodeName
			diagnostic.Name = attributeValue(described.Node.Attributes, "aria-label")
			if diagnostic.Name == "" {
				diagnostic.Name = attributeValue(described.Node.Attributes, "id")
			}
		}
	}
	diagnostic.SuggestedAction = "close the covering element and retry the click"
	return diagnostic, nil
}

func sameTarget(target engine.InteractionTarget, nodeID, backendNodeID int64) bool {
	if target.BackendNodeID != 0 && backendNodeID != 0 {
		return target.BackendNodeID == backendNodeID
	}
	return target.NodeID != "" && target.NodeID == fmt.Sprint(nodeID)
}

func attributeValue(attributes []string, name string) string {
	for i := 0; i+1 < len(attributes); i += 2 {
		if attributes[i] == name {
			return attributes[i+1]
		}
	}
	return ""
}
