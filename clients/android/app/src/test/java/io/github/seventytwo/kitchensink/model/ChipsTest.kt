package io.github.seventytwo.kitchensink.model

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class ChipsTest {
    private fun chip(label: Float, value: Float) = ChipMeasure(label, 10f, value, 14f)

    @Test
    fun sharesOneWidth() {
        val l = layoutChips(listOf(chip(20f, 30f), chip(40f, 30f), chip(10f, 10f)), 500f, 6f, 4f, 4f, 44f)
        // All as wide as the widest, 40 + padding, on one row.
        assertEquals(listOf(52f, 52f, 52f), l.widths)
        assertEquals(listOf(0f, 56f, 112f), l.x)
        assertTrue(l.rowStart[0] && !l.rowStart[1])
        assertEquals(4f * 2 + 10f + 14f, l.height)
        assertEquals(l.height, l.total)
    }

    @Test
    fun narrowsToFitOneRow() {
        // Six chips 72 wide for their details in 300: an equal share is
        // (300 - 20) / 6 = 46.7, enough for their labels and values, so
        // they narrow rather than wrap.
        val l = layoutChips(List(6) { ChipMeasure(20f, 10f, 20f, 14f, 60f, 10f) }, 300f, 6f, 4f, 4f, 44f)
        assertTrue(l.y.all { it == 0f })
        assertEquals(280f / 6, l.widths[0], 0.01f)
    }

    @Test
    fun aWideValueKeepsItsWidth() {
        // One chip needs 90; the rest share what's left.
        val ms = listOf(chip(20f, 78f)) + List(4) { chip(20f, 28f) }
        val l = layoutChips(ms, 300f, 6f, 4f, 4f, 44f)
        assertEquals(90f, l.widths[0])
        assertEquals((300f - 16f - 90f) / 4, l.widths[1], 0.01f)
        assertTrue(l.y.all { it == 0f })
    }

    @Test
    fun wrapsWhenTheyCantFit() {
        val l = layoutChips(List(8) { chip(80f, 20f) }, 300f, 6f, 4f, 4f, 44f)
        // 92 wide, three to a row of 300.
        assertEquals(listOf(true, false, false, true, false, false, true, false), l.rowStart)
        assertEquals(3 * l.height + 2 * 4f, l.total)
    }
}

