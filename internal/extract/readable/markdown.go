package readable

import (
	"fmt"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
)

// Markdown converts the extracted main HTML to Markdown. A new converter is
// used for each call so an Article can be converted concurrently with others.
func (a Article) Markdown() (string, error) {
	conv := htmltomarkdown.NewConverter(
		htmltomarkdown.WithPlugins(
			base.NewBasePlugin(),
			commonmark.NewCommonmarkPlugin(
				// Extract removes unsafe targets. Render any other targetless
				// anchor as text rather than as a misleading empty link.
				commonmark.WithLinkEmptyHrefBehavior("skip"),
			),
			table.NewTablePlugin(),
		),
	)
	markdown, err := conv.ConvertString(a.HTML)
	if err != nil {
		return "", fmt.Errorf("convert main content to markdown: %w", err)
	}
	return markdown, nil
}
