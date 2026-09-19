package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

func writeGraphDOT(output io.Writer, export graphExport) error {
	var builder strings.Builder
	builder.WriteString("digraph jejak {\n")
	builder.WriteString("  graph [")
	builder.WriteString(strings.Join([]string{
		dotAttribute("schema_version", strconv.Itoa(export.SchemaVersion)),
		dotAttribute("query_kind", export.Query.Kind),
		dotAttribute("query", export.Query.Value),
		dotAttribute("repository", export.Repository.ID),
		dotAttribute("worktree", export.Worktree.ID),
		dotAttribute("generation", strconv.FormatInt(export.Generation.ID, 10)),
		dotAttribute("commit", export.Generation.Commit),
		dotAttribute("source_mode", export.Source.Mode),
		dotAttribute("base_commit", export.Source.BaseCommit),
		dotAttribute("base_generation_id", strconv.FormatInt(export.Source.BaseGenerationID, 10)),
		dotAttribute("overlay_id", export.Source.OverlayID),
		dotAttribute("manifest_id", export.Source.ManifestID),
	}, ", "))
	builder.WriteString("];\n")

	for _, node := range export.Nodes {
		attributes := []string{
			dotAttribute("label", graphNodeLabel(node)),
			dotAttribute("kind", node.Kind),
			dotAttribute("historical", strconv.FormatBool(node.Historical)),
			dotAttribute("exported", strconv.FormatBool(node.Exported)),
			dotAttribute("is_test", strconv.FormatBool(node.IsTest)),
		}
		for _, attribute := range []struct {
			name  string
			value string
		}{
			{name: "package", value: node.Package},
			{name: "file", value: node.File},
			{name: "blob_sha", value: node.BlobSHA},
			{name: "signature", value: node.Signature},
			{name: "receiver", value: node.Receiver},
			{name: "change_kind", value: node.ChangeKind},
		} {
			if attribute.value != "" {
				attributes = append(attributes, dotAttribute(attribute.name, attribute.value))
			}
		}
		builder.WriteString("  ")
		builder.WriteString(dotQuote(node.ID))
		builder.WriteString(" [")
		builder.WriteString(strings.Join(attributes, ", "))
		builder.WriteString("];\n")
	}

	for _, edge := range export.Edges {
		attributes := []string{
			dotAttribute("label", graphEdgeLabel(edge)),
			dotAttribute("kind", edge.Kind),
			dotAttribute("confidence", edge.Confidence),
			dotAttribute("historical", strconv.FormatBool(edge.Historical)),
		}
		if locations := graphEdgeLocations(edge); locations != "" {
			attributes = append(attributes, dotAttribute("locations", locations))
		}
		if details := strings.Join(edge.Details, "; "); details != "" {
			attributes = append(attributes, dotAttribute("details", details))
		}
		builder.WriteString("  ")
		builder.WriteString(dotQuote(edge.Source))
		builder.WriteString(" -> ")
		builder.WriteString(dotQuote(edge.Target))
		builder.WriteString(" [")
		builder.WriteString(strings.Join(attributes, ", "))
		builder.WriteString("];\n")
	}

	builder.WriteString("}\n")
	if _, err := io.WriteString(output, builder.String()); err != nil {
		return fmt.Errorf("write graph DOT: %w", err)
	}
	return nil
}

func graphNodeLabel(node graphExportNode) string {
	name := node.Name
	if name == "" {
		name = node.ID
	}
	parts := []string{name}
	if node.Kind != "" {
		parts = append(parts, "kind="+node.Kind)
	}
	if node.File != "" {
		parts = append(parts, "file="+node.File)
	}
	if node.Position.StartLine > 0 {
		location := fmt.Sprintf("line=%d", node.Position.StartLine)
		if node.Position.EndLine > 0 && node.Position.EndLine != node.Position.StartLine {
			location = fmt.Sprintf("line=%d-%d", node.Position.StartLine, node.Position.EndLine)
		}
		parts = append(parts, location)
	}
	if node.Historical {
		marker := "historical"
		if node.ChangeKind != "" {
			marker += "=" + node.ChangeKind
		}
		parts = append(parts, marker)
	}
	return strings.Join(parts, "\n")
}

func graphEdgeLabel(edge graphExportEdge) string {
	parts := []string{edge.Kind, "confidence=" + edge.Confidence}
	if edge.Historical {
		parts = append(parts, "historical")
	}
	for _, detail := range edge.Details {
		parts = append(parts, "details="+detail)
	}
	return strings.Join(parts, "\n")
}

func graphEdgeLocations(edge graphExportEdge) string {
	locations := make([]string, 0, len(edge.Locations))
	for _, location := range edge.Locations {
		if location.Path == "" {
			continue
		}
		value := location.Path
		if location.StartLine > 0 {
			value += ":" + strconv.Itoa(location.StartLine)
			if location.EndLine > 0 && location.EndLine != location.StartLine {
				value += "-" + strconv.Itoa(location.EndLine)
			}
		}
		locations = append(locations, value)
	}
	return strings.Join(locations, ", ")
}

func dotAttribute(name, value string) string {
	return name + "=" + dotQuote(value)
}

func dotQuote(value string) string {
	var builder strings.Builder
	builder.Grow(len(value) + 2)
	builder.WriteByte('"')
	for _, character := range value {
		switch character {
		case '\\':
			builder.WriteString(`\\`)
		case '"':
			builder.WriteString(`\"`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case 0:
			builder.WriteString(`\x00`)
		default:
			if character < 0x20 {
				builder.WriteString(`\x`)
				builder.WriteString(fmt.Sprintf("%02x", character))
				continue
			}
			builder.WriteRune(character)
		}
	}
	builder.WriteByte('"')
	return builder.String()
}
