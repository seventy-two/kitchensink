package io.github.seventytwo.kitchensink.model

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class LayoutTest {
    private fun shows(f: Fit) = f.show.joinToString("") { if (it == Show.Full) "F" else if (it == Show.Clipped) "C" else "r" }

    @Test
    fun fitsPanels() {
        // Rows of 50, gaps of 10: 300 high holds all four rows (240) with 60 over.
        val p = mutableListOf(Panel(50f, 150f, 6f), Panel(50f, 600f, 6f), Panel(50f, 80f, 6f), Panel(50f, 100f, 6f))
        // Plenty of room: everything opens.
        assertEquals("FFFF", shows(fitPanels(p, 2000f, 10f, 40f)))
        // The first opens (+100), the second wouldn't fit, the third and
        // fourth open (+30, +50); the second takes what's left: 500 - 420 - 6 = 74.
        val f = fitPanels(p, 500f, 10f, 40f)
        assertEquals("FCFF", shows(f))
        assertEquals(74f, f.body)
        // At 400 the fourth no longer fits, and 24 is too little for a useful scrolling panel.
        assertEquals("FrFr", shows(fitPanels(p, 400f, 10f, 40f)))
        // Pins win: a closed one stays closed and an open one opens first,
        // even when it doesn't fit.
        p[0] = p[0].copy(pin = Pin.Closed)
        p[1] = p[1].copy(pin = Pin.Open)
        assertEquals("rFrr", shows(fitPanels(p, 500f, 10f, 40f)))
        // Unmeasured panels stay as rows.
        assertEquals("rF", shows(fitPanels(listOf(Panel(50f, 0f, 6f), Panel(50f, 100f, 6f)), 2000f, 10f, 40f)))
    }

    @Test
    fun pinsOnlyWhatDiffersFromAutomatic() {
        val p = listOf(Panel(50f, 150f, 6f), Panel(50f, 600f, 6f))
        // At 300 the first opens by itself, the second doesn't fit, and 74
        // left over is too little for it to scroll in.
        assertEquals(Pin.Closed, repin(p, 0, open = false, 300f, 10f, 100f))
        assertEquals(Pin.Open, repin(p, 1, open = true, 300f, 10f, 100f))
        // Opening the first again, once pinned closed, just goes back to automatic.
        val closed = listOf(p[0].copy(pin = Pin.Closed), p[1])
        assertEquals(Pin.Auto, repin(closed, 0, open = true, 300f, 10f, 100f))
    }

    @Test
    fun arrangesPanels() {
        val defaults = listOf("calendar", "todo", "stocks")
        assertEquals(defaults, arrange(defaults, emptyList()))
        assertEquals(listOf("stocks", "calendar", "todo"), arrange(defaults, listOf("stocks", "calendar", "todo")))
        // Gone panels are ignored; new ones follow the panel they follow by default.
        assertEquals(listOf("stocks", "calendar", "todo"), arrange(defaults, listOf("stocks", "gone", "calendar")))
        assertEquals(listOf("new", "b", "a"), arrange(listOf("new", "a", "b"), listOf("b", "a")))

        val order = listOf("a", "b", "c", "d")
        assertEquals(listOf("a", "c", "b", "d"), shift(order, "b", 1))
        assertEquals(listOf("b", "a", "c", "d"), shift(order, "b", -1))
        assertEquals(order, shift(order, "a", -1))
        assertEquals(order, shift(order, "d", 1))
        assertEquals(order, shift(order, "x", 1))
    }

    @Test
    fun oledCare() {
        val points = (0 until 8).map { oledShift(it * 180L, 6f) }.toSet()
        assertEquals(8, points.size)
        for ((x, y) in points) assertTrue(x * x + y * y <= 37)
        assertEquals(6f to 0f, oledShift(0, 6f))
        assertEquals(oledShift(0, 6f), oledShift(179, 6f))
        assertEquals(0f to 6f, oledShift(2 * 180, 6f))
        assertEquals(oledShift(0, 6f), oledShift(8 * 180, 6f))

        assertFalse(oledSwapSides(0))
        assertTrue(oledSwapSides(3600))
        assertFalse(oledSwapSides(7200 + 59))

        assertFalse(oledDim(1000, 1000, 0))
        assertFalse(oledDim(1000 + 599, 1000, 0))
        assertTrue(oledDim(1000 + 600, 1000, 0))
        assertFalse(oledDim(1000 + 600, 1000, 1000 + 700)) // woken by something new
    }

    @Test
    fun choosesTheLayout() {
        assertTrue(useCompact(false, 411f, 891f)) // a phone, upright
        assertTrue(useCompact(false, 891f, 411f)) // on its side
        assertTrue(useCompact(false, 960f, 540f)) // a 1080p TV
        assertFalse(useCompact(false, 1024f, 1280f)) // a big tablet, upright
        // Near the edge it keeps what it has, so it doesn't flicker.
        assertTrue(useCompact(true, 1024f, 910f))
        assertFalse(useCompact(false, 1024f, 890f))
        assertFalse(feedBeside(411f))
        assertTrue(feedBeside(891f))
    }
}
