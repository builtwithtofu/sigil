package planfmt

import "testing"

func TestOutputReviewNestedLocalWorkdirsWithoutTransportTable(t *testing.T) {
	for _, transport := range []string{"", "local"} {
		t.Run(transport, func(t *testing.T) {
			branch := func(parent string) Step {
				return Step{Tree: &CommandNode{
					Decorator: "@fs.workdir", TransportID: transport,
					Args: []Arg{{Key: "path", Val: Value{Kind: ValueString, Str: parent}}},
					Block: []Step{{Tree: &CommandNode{
						Decorator: "@fs.workdir", TransportID: transport,
						Args: []Arg{{Key: "path", Val: Value{Kind: ValueString, Str: "leaf"}}},
						Block: []Step{{Tree: &RedirectNode{
							Source: &CommandNode{Decorator: "@shell", TransportID: transport},
							Target: EndpointSpec{Decorator: "@file", TransportID: transport,
								Args: []Arg{{Key: "path", Val: Value{Kind: ValueString, Str: "out"}}}},
						}}},
					}}},
				}}
			}
			plan := &Plan{Steps: []Step{{Tree: &CommandNode{
				Decorator: "@exec.parallel", TransportID: transport,
				Block: []Step{branch("one"), branch("two")},
			}}}}
			if _, err := ReviewOutputs(plan); err != nil {
				t.Fatalf("distinct parent directories must stay distinct: %v", err)
			}
		})
	}
}
