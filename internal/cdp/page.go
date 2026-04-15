package cdp

import (
	"context"
	"fmt"
)

// Sender is the minimal interface implemented by both *Client (browser-level)
// and *Session (tab-level). All CDP command helpers accept a Sender so they
// work transparently in both contexts.
type Sender interface {
	Send(ctx context.Context, method string, params any, result any) error
	Subscribe(method string) <-chan Event
}

// PrintToPDFParams maps to Page.printToPDF CDP command parameters.
type PrintToPDFParams struct {
	Landscape           bool    `json:"landscape,omitempty"`
	PrintBackground     bool    `json:"printBackground,omitempty"`
	Scale               float64 `json:"scale,omitempty"`
	PaperWidth          float64 `json:"paperWidth,omitempty"`
	PaperHeight         float64 `json:"paperHeight,omitempty"`
	MarginTop           float64 `json:"marginTop,omitempty"`
	MarginBottom        float64 `json:"marginBottom,omitempty"`
	MarginLeft          float64 `json:"marginLeft,omitempty"`
	MarginRight         float64 `json:"marginRight,omitempty"`
	PreferCSSPageSize   bool    `json:"preferCSSPageSize,omitempty"`
	DisplayHeaderFooter bool    `json:"displayHeaderFooter,omitempty"`
	HeaderTemplate      string  `json:"headerTemplate,omitempty"`
	FooterTemplate      string  `json:"footerTemplate,omitempty"`
	PageRanges          string  `json:"pageRanges,omitempty"`
	TransferMode        string  `json:"transferMode,omitempty"`
}

// PrintToPDFResult holds the base64-encoded PDF returned by Chromium.
type PrintToPDFResult struct {
	Data string `json:"data"`
}

// Navigate sends Page.navigate.
func Navigate(ctx context.Context, s Sender, url string) error {
	return s.Send(ctx, "Page.navigate", map[string]any{"url": url}, nil)
}

// SetDocumentContent sets the HTML content of the current page.
// It resolves the main frame ID via Page.getFrameTree automatically.
func SetDocumentContent(ctx context.Context, s Sender, html string) error {
	frameID, err := mainFrameID(ctx, s)
	if err != nil {
		return fmt.Errorf("get main frame id: %w", err)
	}
	return s.Send(ctx, "Page.setDocumentContent", map[string]any{
		"frameId": frameID,
		"html":    html,
	}, nil)
}

// mainFrameID returns the top-level frame ID via Page.getFrameTree.
func mainFrameID(ctx context.Context, s Sender) (string, error) {
	var result struct {
		FrameTree struct {
			Frame struct {
				ID string `json:"id"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	if err := s.Send(ctx, "Page.getFrameTree", nil, &result); err != nil {
		return "", err
	}
	return result.FrameTree.Frame.ID, nil
}

// PrintToPDF triggers PDF generation and returns the base64-encoded result.
func PrintToPDF(ctx context.Context, s Sender, params PrintToPDFParams) (*PrintToPDFResult, error) {
	var result PrintToPDFResult
	if err := s.Send(ctx, "Page.printToPDF", params, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// EnablePage enables Page domain events.
func EnablePage(ctx context.Context, s Sender) error {
	return s.Send(ctx, "Page.enable", nil, nil)
}

// EnableNetwork enables Network domain events.
func EnableNetwork(ctx context.Context, s Sender) error {
	return s.Send(ctx, "Network.enable", nil, nil)
}

// CloseTarget closes the browser target (tab).
// Should be sent on the browser-level *Client, not a Session.
func CloseTarget(ctx context.Context, s Sender, targetID string) error {
	return s.Send(ctx, "Target.closeTarget", map[string]any{"targetId": targetID}, nil)
}
