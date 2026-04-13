package cdp

import "context"

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
	TransferMode        string  `json:"transferMode,omitempty"`
}

// PrintToPDFResult holds the base64-encoded PDF returned by Chromium.
type PrintToPDFResult struct {
	Data string `json:"data"`
}

// Navigate sends Page.navigate and waits for Page.loadEventFired.
func Navigate(ctx context.Context, c *Client, url string) error {
	params := map[string]any{"url": url}
	return c.Send(ctx, "Page.navigate", params, nil)
}

// SetDocumentContent sets the HTML content of the current page directly.
func SetDocumentContent(ctx context.Context, c *Client, html string) error {
	params := map[string]any{
		"frameId": "", // TODO: obtain frameId from Page.getFrameTree
		"html":    html,
	}
	return c.Send(ctx, "Page.setDocumentContent", params, nil)
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

// EnableNetwork enables Network domain events.
func EnableNetwork(ctx context.Context, c *Client) error {
	return c.Send(ctx, "Network.enable", nil, nil)
}

// CloseTarget closes the browser target (tab).
func CloseTarget(ctx context.Context, c *Client, targetID string) error {
	params := map[string]any{"targetId": targetID}
	return c.Send(ctx, "Target.closeTarget", params, nil)
}
