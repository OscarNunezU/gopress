package cdp

import (
	"context"
	"fmt"
)

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
func Navigate(ctx context.Context, c *Client, url string) error {
	params := map[string]any{"url": url}
	return c.Send(ctx, "Page.navigate", params, nil)
}

// SetDocumentContent sets the HTML content of the current page.
// It resolves the main frame ID via Page.getFrameTree automatically.
func SetDocumentContent(ctx context.Context, c *Client, html string) error {
	frameID, err := mainFrameID(ctx, c)
	if err != nil {
		return fmt.Errorf("get main frame id: %w", err)
	}
	params := map[string]any{
		"frameId": frameID,
		"html":    html,
	}
	return c.Send(ctx, "Page.setDocumentContent", params, nil)
}

// mainFrameID returns the top-level frame ID via Page.getFrameTree.
func mainFrameID(ctx context.Context, c *Client) (string, error) {
	var result struct {
		FrameTree struct {
			Frame struct {
				ID string `json:"id"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	if err := c.Send(ctx, "Page.getFrameTree", nil, &result); err != nil {
		return "", err
	}
	return result.FrameTree.Frame.ID, nil
}

// PrintToPDF triggers PDF generation and returns the base64-encoded result.
func PrintToPDF(ctx context.Context, c *Client, params PrintToPDFParams) (*PrintToPDFResult, error) {
	var result PrintToPDFResult
	if err := c.Send(ctx, "Page.printToPDF", params, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// EnablePage enables Page domain events.
func EnablePage(ctx context.Context, c *Client) error {
	return c.Send(ctx, "Page.enable", nil, nil)
}

// EnableLifecycleEvents enables Page.lifecycleEvent notifications.
// Must be called after EnablePage. Required to receive networkIdle events.
func EnableLifecycleEvents(ctx context.Context, c *Client) error {
	return c.Send(ctx, "Page.setLifecycleEventsEnabled", map[string]any{"enabled": true}, nil)
}

// EnableNetwork enables Network domain events.
func EnableNetwork(ctx context.Context, c *Client) error {
	return c.Send(ctx, "Network.enable", nil, nil)
}

// CloseTarget closes the browser target (tab).
func CloseTarget(ctx context.Context, c *Client, targetID string) error {
	params := map[string]any{"targetId": targetID}
	return c.Send(ctx, "Target.closeTarget", params, nil)
}
