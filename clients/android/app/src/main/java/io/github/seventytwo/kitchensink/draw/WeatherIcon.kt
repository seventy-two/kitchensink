// Small weather pictures for the forecast: sun, moon, cloud, rain and so
// on, drawn from shapes so they take the theme's colours. A port of the
// Windows client's weather_icon.cpp.
package io.github.seventytwo.kitchensink.draw

import androidx.compose.ui.graphics.Color
import kotlin.math.PI
import kotlin.math.cos
import kotlin.math.sin

data class IconColours(
    val sun: Color,
    val moon: Color,
    val cloud: Color,
    val darkCloud: Color,
    val rain: Color,
    val snow: Color,
    val bolt: Color,
)

private val names = setOf("sun", "moon", "sun-cloud", "moon-cloud", "cloud", "fog", "drizzle", "rain", "showers", "snow", "storm")

/** Whether [name] is an icon [drawWeatherIcon] knows. */
fun isWeatherIcon(name: String) = name in names

/**
 * Draws in the icon's own units, 0 to 1 across its square, through a shift
 * and scale about the middle, so a shape can be drawn smaller or to one
 * side, e.g. a cloud in front of the sun.
 */
private data class Pen(val c: Shapes, val at: Vec2, val size: Float, val k: Float = 1f, val shiftX: Float = 0f, val shiftY: Float = 0f) {
    fun p(x: Float, y: Float) = Vec2(at.x + ((x - 0.5f) * k + 0.5f + shiftX) * size, at.y + ((y - 0.5f) * k + 0.5f + shiftY) * size)
    fun d(v: Float) = v * k * size
    fun circle(x: Float, y: Float, r: Float, col: Color) = c.circle(p(x, y), d(r), col)
    fun line(x0: Float, y0: Float, x1: Float, y1: Float, w: Float, col: Color) = c.line(p(x0, y0), p(x1, y1), d(w), col)
    fun poly(pts: List<Vec2>, col: Color) = c.polygon(pts.map { p(it.x, it.y) }, col)
}

/** A cloud spans 0.15 to 0.89 across and 0.27 to 0.77 down, flat beneath. */
private fun cloud(pen: Pen, col: Color) {
    pen.circle(0.32f, 0.60f, 0.17f, col)
    pen.circle(0.54f, 0.48f, 0.21f, col)
    pen.circle(0.73f, 0.61f, 0.16f, col)
    pen.poly(listOf(Vec2(0.32f, 0.55f), Vec2(0.73f, 0.55f), Vec2(0.73f, 0.77f), Vec2(0.32f, 0.77f)), col)
}

private fun sun(pen: Pen, x: Float, y: Float, r: Float, col: Color) {
    pen.circle(x, y, r, col)
    for (i in 0 until 8) {
        val a = i * PI.toFloat() / 4
        val cs = cos(a)
        val sn = sin(a)
        pen.line(x + cs * r * 1.45f, y + sn * r * 1.45f, x + cs * r * 2.05f, y + sn * r * 2.05f, r * 0.35f, col)
    }
}

/** A crescent, lit on the left: slices between its outer edge, a half circle, and its inner one, a narrower half ellipse. */
private fun moon(pen: Pen, x: Float, y: Float, r: Float, col: Color) {
    val n = 16
    fun edge(i: Int, k: Float): Vec2 {
        val a = PI.toFloat() * (0.5f + i.toFloat() / n)
        return Vec2(x + cos(a) * r * k, y + sin(a) * r)
    }
    for (i in 0 until n) pen.poly(listOf(edge(i, 1f), edge(i + 1, 1f), edge(i + 1, 0.15f), edge(i, 0.15f)), col)
}

/** A cloud raised to leave room beneath it for rain or snow. */
private fun raised(pen: Pen) = pen.copy(shiftY = pen.shiftY - 0.17f)

private fun drops(pen: Pen, len: Float, w: Float, col: Color) {
    for (x in listOf(0.36f, 0.54f, 0.72f)) pen.line(x, 0.68f, x - len * 0.35f, 0.68f + len, w, col)
}

/** Draws the named icon in the square at [topLeft], [size] across. Unknown names draw nothing. */
fun drawWeatherIcon(c: Shapes, name: String, topLeft: Vec2, size: Float, col: IconColours) {
    val pen = Pen(c, topLeft, size)
    when (name) {
        "sun" -> sun(pen, 0.5f, 0.5f, 0.2f, col.sun)
        "moon" -> moon(pen, 0.58f, 0.5f, 0.32f, col.moon)
        "sun-cloud", "moon-cloud" -> {
            if (name == "sun-cloud") sun(pen, 0.33f, 0.33f, 0.13f, col.sun) else moon(pen, 0.38f, 0.3f, 0.22f, col.moon)
            cloud(pen.copy(k = 0.85f, shiftX = 0.08f, shiftY = 0.06f), col.cloud)
        }
        "cloud" -> cloud(pen, col.cloud)
        "fog" -> {
            pen.line(0.18f, 0.38f, 0.82f, 0.38f, 0.07f, col.cloud)
            pen.line(0.26f, 0.54f, 0.9f, 0.54f, 0.07f, col.cloud)
            pen.line(0.12f, 0.70f, 0.72f, 0.70f, 0.07f, col.cloud)
        }
        "drizzle" -> {
            cloud(raised(pen), col.cloud)
            drops(pen, 0.12f, 0.065f, col.rain)
        }
        "rain" -> {
            cloud(raised(pen), col.darkCloud)
            drops(pen, 0.22f, 0.065f, col.rain)
        }
        "showers" -> {
            sun(pen, 0.68f, 0.21f, 0.09f, col.sun)
            cloud(raised(pen), col.cloud)
            drops(pen, 0.16f, 0.065f, col.rain)
        }
        "snow" -> {
            cloud(raised(pen), col.cloud)
            for (s in listOf(Vec2(0.34f, 0.72f), Vec2(0.54f, 0.82f), Vec2(0.74f, 0.72f), Vec2(0.44f, 0.92f), Vec2(0.64f, 0.92f))) {
                pen.circle(s.x, s.y, 0.05f, col.snow)
            }
        }
        "storm" -> {
            cloud(raised(pen), col.darkCloud)
            pen.poly(listOf(Vec2(0.58f, 0.58f), Vec2(0.42f, 0.78f), Vec2(0.56f, 0.78f)), col.bolt)
            pen.poly(listOf(Vec2(0.50f, 0.74f), Vec2(0.64f, 0.74f), Vec2(0.46f, 0.96f)), col.bolt)
        }
    }
}
