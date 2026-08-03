package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	cdproto "github.com/chromedp/cdproto"
	"github.com/danieljustus/symaira-browse/internal/engine"
)

// Inspect evaluates a DOM/page inspection without exposing CDP result types at
// the engine boundary.
func (e *Engine) Inspect(ctx context.Context, page engine.Page, request engine.InspectionRequest, target *engine.InteractionTarget) (engine.InspectionResult, error) {
	if target == nil {
		expression, err := inspectionExpression(request)
		if err != nil {
			return engine.InspectionResult{}, err
		}
		result, err := e.Evaluate(ctx, page, expression)
		if err != nil {
			return engine.InspectionResult{}, err
		}
		if result.ExceptionText != "" {
			return engine.InspectionResult{}, errors.New(result.ExceptionText)
		}
		return engine.InspectionResult{Kind: request.Kind, Selector: request.Selector, Value: result.Value}, nil
	}
	if target.BackendNodeID == 0 {
		return engine.InspectionResult{}, errors.New("inspection target has no backend node id")
	}

	var resolved struct {
		Object struct {
			ObjectID string `json:"objectId"`
		} `json:"object"`
	}
	if err := e.call(ctx, page.SessionID, cdproto.CommandDOMResolveNode, struct {
		BackendNodeID int64 `json:"backendNodeId"`
	}{target.BackendNodeID}, &resolved); err != nil {
		return engine.InspectionResult{}, fmt.Errorf("resolve inspection target: %w", err)
	}
	if resolved.Object.ObjectID == "" {
		return engine.InspectionResult{}, errors.New("chrome returned an empty inspection object id")
	}

	functionBody, err := inspectionFunctionBody(request)
	if err != nil {
		return engine.InspectionResult{}, err
	}
	var evaluated struct {
		Result struct {
			Value       json.RawMessage `json:"value"`
			Type        string          `json:"type"`
			Description string          `json:"description"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails,omitempty"`
	}
	params := struct {
		ObjectID            string `json:"objectId"`
		FunctionDeclaration string `json:"functionDeclaration"`
		ReturnByValue       bool   `json:"returnByValue"`
		AwaitPromise        bool   `json:"awaitPromise"`
	}{resolved.Object.ObjectID, "function(){" + functionBody + "}", true, true}
	if err := e.call(ctx, page.SessionID, cdproto.CommandRuntimeCallFunctionOn, params, &evaluated); err != nil {
		return engine.InspectionResult{}, fmt.Errorf("inspect element: %w", err)
	}
	if evaluated.ExceptionDetails != nil && evaluated.ExceptionDetails.Text != "" {
		return engine.InspectionResult{}, errors.New(evaluated.ExceptionDetails.Text)
	}
	return engine.InspectionResult{Kind: request.Kind, Selector: request.Selector, Value: evaluated.Result.Value}, nil
}

func inspectionExpression(request engine.InspectionRequest) (string, error) {
	selector, err := json.Marshal(request.Selector)
	if err != nil {
		return "", err
	}
	attribute, err := json.Marshal(request.Attribute)
	if err != nil {
		return "", err
	}
	properties, err := json.Marshal(request.Properties)
	if err != nil {
		return "", err
	}
	value := inspectionValueExpression(request.Kind, "e", string(attribute), string(properties), request.Selector == "")
	if request.Kind == engine.InspectCount {
		return "document.querySelectorAll(" + string(selector) + ").length", nil
	}
	if request.Selector == "" && (request.Kind == engine.InspectTitle || request.Kind == engine.InspectURL) {
		return value, nil
	}
	return "(function(){const e=document.querySelector(" + string(selector) + ");if(!e)throw new Error(" + selectorError(request.Selector) + ");return " + value + ";})()", nil
}

func inspectionFunctionBody(request engine.InspectionRequest) (string, error) {
	attribute, err := json.Marshal(request.Attribute)
	if err != nil {
		return "", err
	}
	properties, err := json.Marshal(request.Properties)
	if err != nil {
		return "", err
	}
	if request.Kind == engine.InspectCount {
		return "return 1;", nil
	}
	return "return " + inspectionValueExpression(request.Kind, "this", string(attribute), string(properties), false) + ";", nil
}

func selectorError(selector string) string {
	message, _ := json.Marshal(fmt.Sprintf("selector %q did not match an element", selector))
	return string(message)
}

func inspectionValueExpression(kind engine.InspectionKind, element, attribute, properties string, pageValue bool) string {
	if pageValue && kind == engine.InspectTitle {
		return "document.title"
	}
	if pageValue && kind == engine.InspectURL {
		return "location.href"
	}
	switch kind {
	case engine.InspectText:
		return "(" + element + ".innerText || " + element + ".textContent || \"\")"
	case engine.InspectHTML:
		return element + ".innerHTML"
	case engine.InspectValue:
		return "(" + element + ".value === undefined ? \"\" : " + element + ".value)"
	case engine.InspectAttr:
		return element + ".getAttribute(" + attribute + ")"
	case engine.InspectTitle:
		return element + ".title || \"\""
	case engine.InspectURL:
		return "(" + element + ".href || " + element + ".getAttribute(\"href\") || \"\")"
	case engine.InspectBox:
		return "(function(r){return {x:r.x,y:r.y,width:r.width,height:r.height,top:r.top,right:r.right,bottom:r.bottom,left:r.left};})(" + element + ".getBoundingClientRect())"
	case engine.InspectStyles:
		return computedStylesExpression(element, properties)
	case engine.InspectVisible:
		return "(function(e){const s=getComputedStyle(e),r=e.getBoundingClientRect();return s.display!==\"none\"&&s.visibility!==\"hidden\"&&s.opacity!==\"0\"&&r.width>0&&r.height>0})(" + element + ")"
	case engine.InspectEnabled:
		return "(" + element + ".disabled !== true && " + element + ".getAttribute(\"aria-disabled\") !== \"true\")"
	case engine.InspectChecked:
		return "(" + element + ".checked === true || " + element + ".getAttribute(\"aria-checked\") === \"true\")"
	default:
		return "null"
	}
}

func computedStylesExpression(element, properties string) string {
	return "(function(e, wanted){const s=getComputedStyle(e),o={};if(wanted&&wanted.length){wanted.forEach(k=>o[k]=s.getPropertyValue(k));}else{for(let i=0;i<s.length;i++){const k=s[i];o[k]=s.getPropertyValue(k);}}return o})(" + element + "," + properties + ")"
}

var _ engine.InspectionEngine = (*Engine)(nil)
