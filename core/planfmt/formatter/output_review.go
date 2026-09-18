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
		fmt.Fprintf(w, "context %s: %s", transport.ID, formatCommandNode(call))
		if transport.ParentID != "" {
			fmt.Fprintf(w, " (parent=%s)", transport.ParentID)
		}
		fmt.Fprintln(w)
	}
	warnings, err := planfmt.ReviewOutputs(plan)
	for _, warning := range warnings {
		fmt.Fprintln(w, "warning:", warning)
	}
	if err != nil {
		fmt.Fprintln(w, "invalid outputs:", err)
	}
}
