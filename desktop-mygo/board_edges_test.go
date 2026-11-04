package main

import (
	"image"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
	"wtable/internal/model"
)

func edgeFixture() *Workbench {
	return &Workbench{preview: &TopologyTemplate{Agents: []TemplateAgent{
		{Key: "a", Name: "Architect", Layout: NodeLayout{X: 97.3125, Y: 52.375}},
		{Key: "b", Name: "Builder", Layout: NodeLayout{X: 426.875, Y: 237.3125}},
		{Key: "c", Name: "Reviewer", Layout: NodeLayout{X: 761.25, Y: 134.875}},
	}, Links: []model.Edge{{From: "a", To: "b"}, {From: "b", To: "a"}, {From: "a", To: "c"}, {From: "c", To: "a"}}}}
}

// These are the user's real diagonal layouts. Sampling the actual renderer
// catches Painter.Line's horizontal/vertical-only behavior at every zoom.
func TestBoardDiagonalEdgesRemainContinuous(t *testing.T) {
	for _, zoom := range []float32{.5, 1, 1.7} {
		w := edgeFixture()
		v := w.boardCamera()
		v.X, v.Y, v.Scale = 20, 20, zoom
		panes := w.boardPanes()
		routes := w.boardEdgeRoutes(panes)
		if len(routes) != 2 {
			t.Fatalf("four directed grants should draw two paths, got %d", len(routes))
		}
		tt := ui.NewTester(func(c *ui.Context) {
			theme := ui.DarkTheme()
			theme.TextMuted = ui.RGB(255, 255, 255)
			ui.Box(c).Fill().Background(ui.RGB(0, 0, 0)).Draw(func(p *ui.Painter, r ui.Rect) { w.boardDrawEdges(p, r, panes, theme) })
		}, 1500, 900)
		pixels := tt.Image()
		for _, route := range routes {
			if !route.AB || !route.BA {
				t.Fatal("bidirectional grants lost an arrow")
			}
			curve := route.Curve.mapped(zoom, 20, 20)
			for sample := 1; sample < 20; sample++ {
				point := curve.at(float32(sample) / 20)
				if !brightNear(pixels, point) {
					t.Fatalf("zoom %.2f: path %s/%s disconnected at t=%.2f (%v)", zoom, route.A, route.B, float32(sample)/20, point)
				}
			}
		}
	}
}

func brightNear(img *image.RGBA, p ui.Point) bool {
	x, y := int(math.Round(float64(p.X))), int(math.Round(float64(p.Y)))
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			if c := img.RGBAAt(x+dx, y+dy); c.R > 80 && c.G > 80 && c.B > 80 {
				return true
			}
		}
	}
	return false
}

func TestBoardRoutesAvoidMiddleNodeAndPreserveDirection(t *testing.T) {
	w := edgeFixture()
	w.preview.Links = []model.Edge{{From: "a", To: "c"}}
	for i := range w.preview.Agents {
		w.preview.Agents[i].Layout = NodeLayout{X: float32(i) * 300}
	}
	panes := w.boardPanes()
	routes := w.boardEdgeRoutes(panes)
	if len(routes) != 1 || !routes[0].AB || routes[0].BA {
		t.Fatalf("incorrect single-direction route: %+v", routes)
	}
	middle := boardNodeRect(w.boardLayout("b"))
	if boardCurveBlocked(routes[0].Curve, []ui.Rect{middle}) {
		t.Fatal("route still crosses the middle node")
	}
	anchor := routes[0].Curve.at(.5)
	if anchor.Y >= middle.Y-12 {
		t.Fatalf("editing anchor must follow outside curve, got %+v", anchor)
	}
	if w.preview.Agents[1].Layout.X != 300 {
		t.Fatal("routing changed a user node position")
	}
}
