// A simple drawing interface for the cat and the weather icons, so they're
// drawn the same way as on Windows and can be tested without a screen.
package io.github.seventytwo.kitchensink.draw

import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.drawscope.DrawScope
import androidx.compose.ui.graphics.drawscope.rotateRad

data class Vec2(val x: Float, val y: Float) {
    operator fun plus(o: Vec2) = Vec2(x + o.x, y + o.y)
    operator fun minus(o: Vec2) = Vec2(x - o.x, y - o.y)
    operator fun times(s: Float) = Vec2(x * s, y * s)
}

/** Thicknesses and radii are in canvas units. */
interface Shapes {
    fun circle(c: Vec2, r: Float, col: Color)
    fun ellipse(c: Vec2, rx: Float, ry: Float, rot: Float, col: Color)
    fun line(a: Vec2, b: Vec2, thickness: Float, col: Color)
    /** Convex, filled. */
    fun polygon(points: List<Vec2>, col: Color)
}

/** Draws [Shapes] with Compose. */
class DrawScopeShapes(private val d: DrawScope) : Shapes {
    private fun o(v: Vec2) = Offset(v.x, v.y)
    override fun circle(c: Vec2, r: Float, col: Color) = d.drawCircle(col, r, o(c))
    override fun ellipse(c: Vec2, rx: Float, ry: Float, rot: Float, col: Color) {
        d.rotateRad(rot, o(c)) { drawOval(col, Offset(c.x - rx, c.y - ry), Size(rx * 2, ry * 2)) }
    }
    override fun line(a: Vec2, b: Vec2, thickness: Float, col: Color) = d.drawLine(col, o(a), o(b), thickness)
    override fun polygon(points: List<Vec2>, col: Color) {
        if (points.size < 3) return
        val p = Path()
        p.moveTo(points[0].x, points[0].y)
        for (i in 1 until points.size) p.lineTo(points[i].x, points[i].y)
        p.close()
        d.drawPath(p, col)
    }
}
