package render

import "github.com/abdul-hamid-achik/vecgrep/internal/search"

type OutputFormat = search.OutputFormat

const (
	FormatDefault = search.FormatDefault
	FormatJSON    = search.FormatJSON
	FormatCompact = search.FormatCompact
)

func Results(results []search.Result, format OutputFormat) string {
	return search.FormatResults(results, format)
}

func ParseOutputFormat(format string) OutputFormat {
	switch format {
	case "json":
		return FormatJSON
	case "compact":
		return FormatCompact
	default:
		return FormatDefault
	}
}
