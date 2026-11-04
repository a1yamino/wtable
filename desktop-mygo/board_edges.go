package main

import (
	"math"

	"github.com/egoist/mygo/ui"
	"wtable/internal/model"
)

// Geometry stays in world coordinates. The same curve supplies the visible
// stroke, terminal arrows, editing anchor and minimap.
type boardCurve struct{ P0, P1, P2, P3 ui.Point }
type boardEdgeRoute struct {
	A, B   string
	AB, BA bool
	Curve  boardCurve
}

func (b boardCurve) at(t float32) ui.Point {
	u := 1 - t
	return ui.Point{X: u*u*u*b.P0.X + 3*u*u*t*b.P1.X + 3*u*t*t*b.P2.X + t*t*t*b.P3.X, Y: u*u*u*b.P0.Y + 3*u*u*t*b.P1.Y + 3*u*t*t*b.P2.Y + t*t*t*b.P3.Y}
}
func (b boardCurve) mapped(scale, x, y float32) boardCurve {
	point := func(p ui.Point) ui.Point { return ui.Point{X: x + p.X*scale, Y: y + p.Y*scale} }
	return boardCurve{point(b.P0), point(b.P1), point(b.P2), point(b.P3)}
}
func (b boardCurve) path() *ui.Path {
	return new(ui.Path).MoveTo(b.P0.X, b.P0.Y).CubeTo(b.P1.X, b.P1.Y, b.P2.X, b.P2.Y, b.P3.X, b.P3.Y)
}
func boardNodeRect(l NodeLayout) ui.Rect {
	width, height := boardSize(l)
	return ui.Rect{X: l.X, Y: l.Y, W: width, H: height}
}
func boardDirectCurve(a, b ui.Rect) boardCurve {
	ax, ay, bx, by := a.X+a.W/2, a.Y+a.H/2, b.X+b.W/2, b.Y+b.H/2
	if math.Abs(float64(bx-ax)) >= math.Abs(float64(by-ay)) {
		direction := float32(1)
		if bx < ax {
			direction = -1
		}
		p0, p3 := ui.Point{X: ax + direction*(a.W/2+4), Y: ay}, ui.Point{X: bx - direction*(b.W/2+4), Y: by}
		handle := boardClamp(float32(math.Abs(float64(p3.X-p0.X)))*.5, 30, 180)
		return boardCurve{p0, ui.Point{X: p0.X + direction*handle, Y: p0.Y}, ui.Point{X: p3.X - direction*handle, Y: p3.Y}, p3}
	}
	direction := float32(1)
	if by < ay {
		direction = -1
	}
	p0, p3 := ui.Point{X: ax, Y: ay + direction*(a.H/2+4)}, ui.Point{X: bx, Y: by - direction*(b.H/2+4)}
	handle := boardClamp(float32(math.Abs(float64(p3.Y-p0.Y)))*.5, 30, 180)
	return boardCurve{p0, ui.Point{X: p0.X, Y: p0.Y + direction*handle}, ui.Point{X: p3.X, Y: p3.Y - direction*handle}, p3}
}
func boardCurveBlocked(curve boardCurve, obstacles []ui.Rect) bool {
	for step := 0; step <= 64; step++ {
		p := curve.at(float32(step) / 64)
		for _, r := range obstacles {
			if p.X > r.X-12 && p.X < r.X+r.W+12 && p.Y > r.Y-12 && p.Y < r.Y+r.H+12 {
				return true
			}
		}
	}
	return false
}
func boardRoutedCurve(a, b ui.Rect, obstacles []ui.Rect) boardCurve {
	direct := boardDirectCurve(a, b)
	if !boardCurveBlocked(direct, obstacles) {
		return direct
	}
	// Only the direct third-node obstruction needs a detour. Try the two
	// outside lanes rather than introducing a general graph router.
	top, bottom := min(a.Y, b.Y), max(a.Y+a.H, b.Y+b.H)
	for _, r := range obstacles {
		top = min(top, r.Y)
		bottom = max(bottom, r.Y+r.H)
	}
	upper := boardCurve{ui.Point{X: a.X + a.W/2, Y: a.Y - 4}, ui.Point{X: a.X + a.W/2, Y: top - 100}, ui.Point{X: b.X + b.W/2, Y: top - 100}, ui.Point{X: b.X + b.W/2, Y: b.Y - 4}}
	if !boardCurveBlocked(upper, obstacles) {
		return upper
	}
	lower := boardCurve{ui.Point{X: a.X + a.W/2, Y: a.Y + a.H + 4}, ui.Point{X: a.X + a.W/2, Y: bottom + 100}, ui.Point{X: b.X + b.W/2, Y: bottom + 100}, ui.Point{X: b.X + b.W/2, Y: b.Y + b.H + 4}}
	if !boardCurveBlocked(lower, obstacles) {
		return lower
	}
	return upper
}
func (w *Workbench) boardEdgeRoutes(panes []model.Pane) []boardEdgeRoute {
	valid := make(map[string]bool, len(panes))
	for _, p := range panes {
		valid[p.ID] = true
	}
	edges := w.boardEdges()
	routes := make([]boardEdgeRoute, 0)
	for _, pair := range boardPairs(edges) {
		if !valid[pair[0]] || !valid[pair[1]] {
			continue
		}
		obstacles := make([]ui.Rect, 0, len(panes)-2)
		for _, p := range panes {
			if p.ID != pair[0] && p.ID != pair[1] {
				obstacles = append(obstacles, boardNodeRect(w.boardLayout(p.ID)))
			}
		}
		routes = append(routes, boardEdgeRoute{A: pair[0], B: pair[1], AB: boardHasEdge(edges, pair[0], pair[1]), BA: boardHasEdge(edges, pair[1], pair[0]), Curve: boardRoutedCurve(boardNodeRect(w.boardLayout(pair[0])), boardNodeRect(w.boardLayout(pair[1])), obstacles)})
	}
	return routes
}

// Include outside detours when framing the graph, so Fit and the minimap
// do not clip an otherwise continuous connection above or below its nodes.
func (w *Workbench) boardGraphBounds(panes []model.Pane) ui.Rect {
	bounds := w.boardBounds(panes)
	for _, edge := range w.boardEdgeRoutes(panes) {
		curve := edge.Curve.path().Bounds()
		x0, y0 := min(bounds.X, curve.X), min(bounds.Y, curve.Y)
		x1, y1 := max(bounds.X+bounds.W, curve.X+curve.W), max(bounds.Y+bounds.H, curve.Y+curve.H)
		bounds = ui.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
	}
	return bounds
}
func boardDrawArrow(p *ui.Painter, tip, control ui.Point, color ui.Color) {
	dx, dy := tip.X-control.X, tip.Y-control.Y
	length := float32(math.Hypot(float64(dx), float64(dy)))
	if length == 0 {
		return
	}
	dx /= length
	dy /= length
	arrow := new(ui.Path).MoveTo(tip.X, tip.Y).LineTo(tip.X-dx*8+dy*3.5, tip.Y-dy*8-dx*3.5).LineTo(tip.X-dx*8-dy*3.5, tip.Y-dy*8+dx*3.5).Close()
	p.FillPath(arrow, color)
}
func (w *Workbench) boardDrawEdges(p *ui.Painter, r ui.Rect, panes []model.Pane, t *ui.Theme) {
	v := w.boardCamera()
	for _, edge := range w.boardEdgeRoutes(panes) {
		curve := edge.Curve.mapped(v.Scale, r.X+v.X, r.Y+v.Y)
		color, width := t.TextMuted.Alpha(.78), float32(1.65)
		if w.board.edgeA == edge.A && w.board.edgeB == edge.B || w.board.edgeA == edge.B && w.board.edgeB == edge.A {
			color, width = t.Accent, float32(2)
			p.StrokePath(curve.path(), 6, t.Accent.Alpha(.09))
		}
		p.StrokePath(curve.path(), width, color)
		p.FillPath(new(ui.Path).Circle(curve.P0.X, curve.P0.Y, 2), color)
		p.FillPath(new(ui.Path).Circle(curve.P3.X, curve.P3.Y, 2), color)
		if edge.AB {
			boardDrawArrow(p, curve.P3, curve.P2, color)
		}
		if edge.BA {
			boardDrawArrow(p, curve.P0, curve.P1, color)
		}
	}
}
