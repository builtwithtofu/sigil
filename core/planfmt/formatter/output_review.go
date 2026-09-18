package formatter

import (
	"fmt"
	"io"
	"strings"

	"github.com/builtwithtofu/sigil/core/planfmt"
)

func formatEndpoint(endpoint planfmt.EndpointSpec) string {
	text := formatCommandNode(endpoint.Command())
	if endpoint.TransportID != "" {
		text += " [context=" + endpoint.TransportID + "]"
	}
	return text
}

func formatOutputReview(w io.Writer, plan *planfmt.Plan) {
	for _, transport := range plan.Transports {
		call := &planfmt.CommandNode{Decorator: "@" + strings.TrimPrefix(transport.Decorator, "@"), Args: transport.Args}
		_, _ = fmt.Fprintf(w, "context %s: %s", transport.ID, formatCommandNode(call))
		if transport.ParentID != "" {
			_, _ = fmt.Fprintf(w, " (parent=%s)", transport.ParentID)
		}
		_, _ = fmt.Fprintln(w)
	}
	warnings, err := planfmt.ReviewOutputs(plan)
	for _, warning := range warnings {
		_, _ = fmt.Fprintln(w, "warning:", warning)
	}
	if err != nil {
		_, _ = fmt.Fprintln(w, "invalid outputs:", err)
	}
}
