package shared

import (
	"fmt"
	"strings"

	"github.com/dracory/nodestore"
)

// ShortID renders an entity ID in the compact form used in list
// columns (first 12 chars — enough to disambiguate visually).
func ShortID(id string) string {
	if len(id) > 12 {
		return id[:12] + "…"
	}
	return id
}

// WorkflowMinimap renders a scaled SVG thumbnail of the workflow
// topology — real node positions normalized into the 120x44 viewBox
// with the stored edges drawn as connectors. Empty workflows render
// the bare dotted thumbnail.
func WorkflowMinimap(wf nodestore.WorkflowInterface) string {
	nodeList := wf.GetNodes()
	if len(nodeList) == 0 {
		return `<div class="wf-minimap"></div>`
	}
	pos := map[string][2]float64{}
	minX, minY := nodeList[0].GetPositionX(), nodeList[0].GetPositionY()
	maxX, maxY := minX, minY
	for _, n := range nodeList {
		x, y := n.GetPositionX(), n.GetPositionY()
		pos[n.GetID()] = [2]float64{x, y}
		if x < minX {
			minX = x
		}
		if x > maxX {
			maxX = x
		}
		if y < minY {
			minY = y
		}
		if y > maxY {
			maxY = y
		}
	}
	spanX := maxX - minX
	spanY := maxY - minY
	if spanX < 1 {
		spanX = 1
	}
	if spanY < 1 {
		spanY = 1
	}
	scale := func(x, y float64) (float64, float64) {
		return 8 + (x-minX)/spanX*104, 6 + (y-minY)/spanY*32
	}
	svg := strings.Builder{}
	svg.WriteString(`<svg viewBox="0 0 120 44">`)
	for _, e := range wf.GetEdges() {
		a, okA := pos[e.GetSource()]
		b, okB := pos[e.GetTarget()]
		if !okA || !okB {
			continue
		}
		x1, y1 := scale(a[0], a[1])
		x2, y2 := scale(b[0], b[1])
		mx := (x1 + x2) / 2
		fmt.Fprintf(&svg, `<path d="M%.1f %.1f C%.1f %.1f %.1f %.1f %.1f %.1f" stroke="currentColor" fill="none" opacity=".5"/>`,
			x1, y1, mx, y1, mx, y2, x2, y2)
	}
	for _, n := range nodeList {
		x, y := scale(pos[n.GetID()][0], pos[n.GetID()][1])
		fmt.Fprintf(&svg, `<circle cx="%.1f" cy="%.1f" r="3" fill="var(--na-accent)"/>`, x, y)
	}
	svg.WriteString(`</svg>`)
	return `<div class="wf-minimap">` + svg.String() + `</div>`
}
