// A black cat who sleeps above the Recent column and every so often wakes,
// stretches, wanders to another spot and curls up again. A port of the
// Windows client's cat.cpp: where she is and how she's lying at any moment
// is worked out here, and she's drawn through [Shapes].
package io.github.seventytwo.kitchensink.draw

import androidx.compose.ui.graphics.Color
import kotlin.math.PI
import kotlin.math.abs
import kotlin.math.cos
import kotlin.math.floor
import kotlin.math.hypot
import kotlin.math.max
import kotlin.math.min
import kotlin.math.sin

private const val Pi = PI.toFloat()

private fun lerp(a: Vec2, b: Vec2, k: Float) = Vec2(a.x + (b.x - a.x) * k, a.y + (b.y - a.y) * k)
private fun lerpf(a: Float, b: Float, k: Float) = a + (b - a) * k
private fun smooth(k: Float): Float {
    val c = k.coerceIn(0f, 1f)
    return c * c * (3 - 2 * c)
}
private fun rotate(v: Vec2, a: Float) = Vec2(v.x * cos(a) - v.y * sin(a), v.x * sin(a) + v.y * cos(a))

/**
 * Her shape, side on, facing right, in units with the floor at y = 0 below
 * her middle and y growing downwards. Her body is three circles, chest to
 * hips, joined smoothly. Legs: front near, front far, back near, back far.
 */
data class CatPose(
    val chest: Vec2, val rChest: Float,
    val mid: Vec2, val rMid: Float,
    val hips: Vec2, val rHips: Float,
    val head: Vec2, val rHead: Float = 8f,
    /** Radians, clockwise: down towards her chest. */
    val headTilt: Float,
    val joint: List<Vec2>,
    val paw: List<Vec2>,
    /** From the base out. */
    val tail: List<Vec2>,
    /** 0 closed to 1 open. */
    val eyes: Float,
    val yawn: Float,
)

/**
 * Her at one moment: her pose, where she is along the floor, which way she
 * faces (1 right, -1 left), how far she's turned round from there (0 to 1,
 * her head leading, towards the viewer) and how asleep she is (1 fast
 * asleep, for her breathing and Z's).
 */
data class Cat(val pose: CatPose, val x: Float = 0f, val facing: Float = 1f, val turn: Float = 0f, val asleep: Float = 1f)

private fun v(vararg xy: Float) = xy.toList().chunked(2).map { Vec2(it[0], it[1]) }

// Her poses, facing right.
private val Stand = CatPose(
    Vec2(13f, -22f), 9.5f, Vec2(0f, -23f), 9f, Vec2(-13f, -22f), 10f, Vec2(25f, -31f), headTilt = 0f,
    joint = v(14f, -16f, 11f, -16f, -13f, -15f, -16f, -15f), paw = v(15f, 0f, 11f, 0f, -12f, 0f, -17f, 0f),
    tail = v(-22f, -25f, -27f, -29f, -30f, -35f, -31f, -42f, -30f, -48f, -27f, -53f, -23f, -55f, -20f, -54f),
    eyes = 1f, yawn = 0f,
)
/** Curled up asleep: head down on her paws, tail wrapped round the front. */
private val Sleep = CatPose(
    Vec2(6f, -10f), 10f, Vec2(-4f, -12f), 11.5f, Vec2(-14f, -10f), 10.5f, Vec2(19f, -7.5f), headTilt = 0.25f,
    joint = v(10f, -4f, 8f, -4f, -12f, -4f, -15f, -4f), paw = v(15f, -1f, 12f, -1f, -8f, -1f, -11f, -1f),
    tail = v(-23f, -9f, -25f, -4f, -21f, -1f, -13f, 0f, -4f, 0f, 4f, 0f, 11f, -1f, 16f, -2f),
    eyes = 0f, yawn = 0f,
)
/** The front stretch: chest down, paws reaching, bottom and tail up, yawning. */
private val Bow = CatPose(
    Vec2(16f, -11f), 8.5f, Vec2(3f, -17f), 8.5f, Vec2(-12f, -25f), 9.5f, Vec2(29f, -14f), headTilt = -0.25f,
    joint = v(17f, -7f, 15f, -7f, -12f, -18f, -15f, -18f), paw = v(32f, 0f, 29f, 0f, -11f, 0f, -15f, 0f),
    tail = v(-21f, -28f, -25f, -34f, -27f, -41f, -27f, -48f, -25f, -54f, -22f, -58f, -18f, -60f, -15f, -59f),
    eyes = 0f, yawn = 1f,
)
/** The arch: back up, legs together. */
private val Arch = CatPose(
    Vec2(12f, -26f), 8.5f, Vec2(0f, -33f), 9f, Vec2(-12f, -26f), 9.5f, Vec2(22f, -30f), headTilt = 0.3f,
    joint = v(12f, -19f, 10f, -19f, -11f, -19f, -13f, -19f), paw = v(8f, 0f, 6f, 0f, -7f, 0f, -9f, 0f),
    tail = v(-20f, -29f, -24f, -35f, -26f, -42f, -26f, -48f, -24f, -54f, -21f, -58f, -17f, -60f, -14f, -59f),
    eyes = 0.6f, yawn = 0f,
)

private fun blend(a: CatPose, b: CatPose, k: Float) = CatPose(
    lerp(a.chest, b.chest, k), lerpf(a.rChest, b.rChest, k),
    lerp(a.mid, b.mid, k), lerpf(a.rMid, b.rMid, k),
    lerp(a.hips, b.hips, k), lerpf(a.rHips, b.rHips, k),
    lerp(a.head, b.head, k), lerpf(a.rHead, b.rHead, k), lerpf(a.headTilt, b.headTilt, k),
    a.joint.zip(b.joint) { p, q -> lerp(p, q, k) },
    a.paw.zip(b.paw) { p, q -> lerp(p, q, k) },
    a.tail.zip(b.tail) { p, q -> lerp(p, q, k) },
    lerpf(a.eyes, b.eyes, k), lerpf(a.yawn, b.yawn, k),
)

private fun hash(x0: Int): Int {
    var x = x0
    x = x xor (x ushr 16)
    x *= 0x7feb352d
    x = x xor (x ushr 15)
    x *= 0x846ca68b.toInt()
    x = x xor (x ushr 16)
    return x
}

/** Where she settles at the end of episode [k], from -1 to 1 across the floor. */
private fun spot(k: Long): Float = (Integer.remainderUnsigned(hash((k.toInt() * 2654435761L.toInt())), 2001)) / 1000f - 1

private fun sign(v: Float) = if (v < 0) -1f else 1f

// Lengths of each part of waking up, in seconds.
private const val Wake = 1.4f
private const val ToBow = 0.8f
private const val BowHold = 1.4f
private const val ToArch = 0.9f
private const val ArchHold = 0.9f
private const val ToStand = 0.7f
private const val Turn = 1.3f
private const val Pause = 0.5f
private const val LieDown = 1.8f
private const val Speed = 22f // units a second, walking
private const val Stride = 20f // units a step cycle
private const val MinWalk = 10f // closer than this, she doesn't bother

/** She sleeps for most of each episode, in seconds. */
const val CatEpisode = 100.0

/** Moves her legs, body and tail for a step cycle at [phase]. */
private fun walking(p: CatPose, phase: Float, t: Double): CatPose {
    val offsets = floatArrayOf(0f, Pi, Pi, 0f) // diagonal pairs together
    val paw = p.paw.mapIndexed { i, q ->
        val a = phase + offsets[i]
        Vec2(q.x + 6 * sin(a), q.y - max(0f, cos(a)) * 4)
    }
    val bob = 0.8f * sin(2 * phase)
    val tail = p.tail.mapIndexed { i, q -> Vec2(q.x + sin(t.toFloat() * 2.2f + i * 0.5f) * i * 0.35f, q.y) }
    return p.copy(
        paw = paw, tail = tail,
        chest = p.chest + Vec2(0f, bob), mid = p.mid + Vec2(0f, bob), hips = p.hips + Vec2(0f, bob),
        head = p.head + Vec2(0f, bob * 0.7f),
    )
}

/** Her sides rise and fall, and now and then her tail tip twitches in her sleep. */
private fun breathing(p: CatPose, t: Double, asleep: Float): CatPose {
    val b = sin(2 * Pi * t.toFloat() / 3.6f) * asleep
    var tail = p.tail
    val twitch = (t % 7.3).toFloat()
    if (twitch < 0.8f) {
        val k = sin(twitch / 0.8f * Pi) * asleep
        tail = tail.mapIndexed { i, q -> if (i >= 5) Vec2(q.x, q.y - k * (i - 4) * 1.5f) else q }
    }
    return p.copy(rMid = p.rMid + 0.7f * b, mid = Vec2(p.mid.x, p.mid.y - 0.4f * b), rChest = p.rChest + 0.3f * b, tail = tail)
}

private data class Rest(val x: Float, val facing: Float)

/**
 * Where she is after episode [k], played from the first: she only walks to
 * her next spot when it's far enough, so where she is and which way she
 * faces depend on every episode before. The last answer is kept, so each
 * frame takes a step at most.
 */
private object Rests {
    private var lastK = -2L
    private var lastRoom = -1f
    private var last = Rest(0f, 1f)

    @Synchronized
    fun after(k: Long, room: Float): Rest {
        var from = -1L
        var r = Rest(spot(-1) * room, 1f)
        if (room == lastRoom && lastK >= -1 && lastK <= k) {
            from = lastK
            r = last
        }
        for (j in from + 1..k) {
            val to = spot(j) * room
            if (abs(to - r.x) >= MinWalk) r = Rest(to, sign(to - r.x))
        }
        lastK = k
        lastRoom = room
        last = r
        return r
    }
}

/** The cat [t] seconds in, with [half] units of floor either side of the middle to wander. */
fun catAt(t: Double, half: Float): Cat {
    val room = max(0f, half - 46) // keep all of her on the floor, nose to tail
    // Start some way into an episode, so she's soon up for the first time.
    val s = t + CatEpisode * 0.75
    val k = floor(s / CatEpisode).toLong()
    val u = (s - k * CatEpisode).toFloat()

    val rest = Rests.after(k - 1, room)
    val from = rest.x
    val to = spot(k) * room
    val facing = rest.facing
    val walkTo = sign(to - from)
    val walks = abs(to - from) >= MinWalk
    val walk = if (walks) abs(to - from) / Speed else 0f
    val turn = if (walks && walkTo != facing) Turn else 0f
    val active = Wake + ToBow + BowHold + ToArch + ArchHold + ToStand + turn + walk + Pause + LieDown
    val sleep = CatEpisode.toFloat() - active

    var at = u - sleep
    fun part(len: Float): Float {
        val p = at / len
        at -= len
        return p
    }
    var cat: Cat
    if (at < 0) {
        cat = Cat(Sleep, from, facing, asleep = 1f)
    } else {
        var p = part(Wake)
        cat = when {
            p < 1 -> Cat(blend(Sleep, Stand, smooth(p)), from, facing, asleep = 1 - smooth(p * 2))
            part(ToBow).also { p = it } < 1 -> Cat(blend(Stand, Bow, smooth(p)), from, facing, asleep = 0f)
            part(BowHold).also { p = it } < 1 -> Cat(Bow, from, facing, asleep = 0f)
            part(ToArch).also { p = it } < 1 -> Cat(blend(Bow, Arch, smooth(p)), from, facing, asleep = 0f)
            part(ArchHold).also { p = it } < 1 -> Cat(Arch, from, facing, asleep = 0f)
            part(ToStand).also { p = it } < 1 -> Cat(blend(Arch, Stand, smooth(p)), from, facing, asleep = 0f)
            // Turning round, towards the viewer: see drawCat.
            turn > 0 && part(turn).also { p = it } < 1 -> Cat(walking(Stand, 2 * Pi * p, t), from, facing, turn = p, asleep = 0f)
            walk > 0 && part(walk).also { p = it } < 1 -> {
                val gone = abs(to - from) * p
                Cat(walking(Stand, 2 * Pi * gone / Stride, t), from + walkTo * gone, walkTo, asleep = 0f)
            }
            else -> {
                val x = if (walks) to else from
                val f = if (walks) walkTo else facing
                p = part(Pause)
                if (p < 1) {
                    Cat(Stand, x, f, asleep = 0f)
                } else {
                    p = min(part(LieDown), 1f)
                    Cat(blend(Stand, Sleep, smooth(p)), x, f, asleep = smooth(p * 1.5f - 0.5f))
                }
            }
        }
    }
    // A blink every few seconds while she's awake.
    var pose = cat.pose
    if (t % 4.1 < 0.14) pose = pose.copy(eyes = 0f)
    return cat.copy(pose = breathing(pose, t, cat.asleep))
}

private val Fur = Color(17, 17, 21)
private val Rim = Color(92, 98, 112, 170)
private val Eye = Color(206, 216, 84)
private val Pupil = Color(8, 8, 10)
private val Lid = Color(120, 124, 136, 220)
private val Mouth = Color(176, 84, 100)
private val Whisker = Color(150, 154, 166, 110)
private val Zee = Color(150, 156, 172)
private val Shadow = Color(0, 0, 0, 55)

/**
 * Maps her pose onto the canvas, mirrored for which way she faces, and
 * turned round about the vertical: pose x is how far forward a point is,
 * and l how far to her side nearer the viewer. While she turns round, her
 * head leads and her hips follow, so each point turns by an angle between
 * theirs, from how far forward it is.
 */
private class Painter(val c: Shapes, val floor: Vec2, val x: Float, val facing: Float, val size: Float) {
    var headTurn = 0f
    var hipsTurn = 0f

    fun angle(f: Float) = lerpf(hipsTurn, headTurn, ((f + 13) / 38).coerceIn(0f, 1f))
    fun m(p: Vec2, l: Float = 0f): Vec2 {
        val a = angle(p.x)
        return Vec2(floor.x + (x + facing * (p.x * cos(a) - l * sin(a))) * size, floor.y + p.y * size)
    }
    /** How far towards the viewer a point is, beside a centre at forward [f0]. */
    fun depth(f: Float, l: Float, f0: Float): Float {
        val a = angle(f0)
        return (f - f0) * sin(a) + l * cos(a)
    }
    fun w(r: Float) = r * size
    fun circle(p: Vec2, r: Float, col: Color, l: Float = 0f) = c.circle(m(p, l), w(r), col)
    fun line(a: Vec2, b: Vec2, t: Float, col: Color, l: Float = 0f) = c.line(m(a, l), m(b, l), w(t), col)
    fun limb(a: Vec2, b: Vec2, t: Float, col: Color, l: Float = 0f) {
        line(a, b, t, col, l)
        circle(a, t / 2, col, l)
        circle(b, t / 2, col, l)
    }
    fun poly(pts: List<Vec2>, col: Color, l: Float = 0f) = c.polygon(pts.map { m(it, l) }, col)
}

/** Her body, legs, tail and head in one colour, grown by [g] units all round: once wide for her rim, then in black over it. */
private fun silhouette(d: Painter, p: CatPose, g: Float, col: Color) {
    // Far legs, then the tail, behind the body. One colour throughout, so
    // what's in front of what doesn't matter.
    d.limb(p.joint[1], p.paw[1], 4.6f + 2 * g, col, -3.5f)
    d.limb(p.joint[3], p.paw[3], 5.2f + 2 * g, col, -4f)
    val n = p.tail.size
    for (i in 0 until n - 1) {
        val t = 4.4f - 1.4f * i / (n - 1)
        d.limb(p.tail[i], p.tail[i + 1], t + 2 * g, col)
    }
    // The body: three circles and the quads joining them.
    fun join(a: Vec2, ra: Float, b: Vec2, rb: Float) {
        val dir = b - a
        val len = max(0.001f, hypot(dir.x, dir.y))
        val nrm = Vec2(-dir.y / len, dir.x / len)
        d.poly(listOf(a + nrm * (ra + g), b + nrm * (rb + g), b - nrm * (rb + g), a - nrm * (ra + g)), col)
    }
    d.circle(p.chest, p.rChest + g, col)
    d.circle(p.mid, p.rMid + g, col)
    d.circle(p.hips, p.rHips + g, col)
    join(p.chest, p.rChest, p.mid, p.rMid)
    join(p.mid, p.rMid, p.hips, p.rHips)
    d.limb(p.joint[0], p.paw[0], 4.6f + 2 * g, col, 3.5f)
    d.limb(p.joint[2], p.paw[2], 5.2f + 2 * g, col, 4f)
    // Neck, head, muzzle and ears.
    d.limb(lerp(p.chest, p.head, 0.35f), lerp(p.chest, p.head, 0.8f), 8 + 2 * g, col)
    fun head(v: Vec2) = p.head + rotate(v, p.headTilt)
    d.circle(p.head, p.rHead + g, col)
    d.circle(head(Vec2(6f, 2.5f)), 4.6f + g, col)
    for (e in listOf(-3.5f, 3.0f)) {
        val l = if (e < 0) -3.4f else 3.4f // side by side, seen face on
        val base1 = head(Vec2(e - 3.6f, -5f))
        val base2 = head(Vec2(e + 3.6f, -5f))
        val tip = head(Vec2(e + 1.2f, -13.5f))
        val mid = (base1 + base2 + tip) * (1f / 3)
        fun grow(v: Vec2): Vec2 {
            val off = v - mid
            val len = max(0.001f, hypot(off.x, off.y))
            return v + off * (g / len * 1.4f)
        }
        d.poly(listOf(grow(base1), grow(tip), grow(base2)), col, l)
    }
}

private fun face(d: Painter, p: CatPose) {
    fun head(v: Vec2) = p.head + rotate(v, p.headTilt)
    // Both eyes, the far one hidden behind her head unless she's turned
    // towards the viewer, and narrower the more side on each is.
    for (l in listOf(2.6f, -2.6f)) {
        val eye = head(Vec2(3.8f, -1.2f))
        val k = ((d.depth(eye.x, l, p.head.x) + 0.5f) / 3).coerceIn(0f, 1f)
        if (k <= 0) continue
        if (p.eyes > 0.15f) {
            d.c.ellipse(d.m(eye, l), d.w(1.9f * (0.5f + 0.5f * k)), d.w(2.4f * p.eyes), 0f, Eye)
            d.c.ellipse(d.m(eye, l), d.w(0.55f), d.w(2.0f * p.eyes), 0f, Pupil)
        } else {
            // Closed: a sleepy curve.
            d.line(head(Vec2(2.2f, -0.6f)), head(Vec2(3.8f, 0.2f)), 0.8f, Lid, l)
            d.line(head(Vec2(3.8f, 0.2f)), head(Vec2(5.4f, -0.6f)), 0.8f, Lid, l)
        }
    }
    if (p.yawn > 0.3f) d.c.ellipse(d.m(head(Vec2(8.5f, 4.6f))), d.w(1.4f), d.w(2.4f * p.yawn), 0f, Mouth)
    // Whiskers both sides, fanning out, the far ones only once she's turned
    // enough to show them.
    val far = sin(d.angle(p.head.x)) > 0.25f
    for (side in listOf(1f, -1f)) {
        if (side < 0 && !far) continue
        for (dy in listOf(-0.6f, 1.0f)) {
            val a = head(Vec2(9f, 2 + dy))
            val b = head(Vec2(15f, 1 + dy * 2.2f))
            d.c.line(d.m(a, side * 1.5f), d.m(b, side * 6), d.w(0.45f), Whisker)
        }
    }
}

/** Z's float up from her head while she's asleep: a small z, rising, growing and fading, one after another. */
private fun zs(d: Painter, cat: Cat, t: Double) {
    if (cat.asleep < 0.5f) return // not once her head is up
    for (i in 0 until 3) {
        val k = ((t / 4.5 + i / 3.0) % 1.0).toFloat()
        val s = 2.2f + 2.6f * k
        val at = cat.pose.head + Vec2(4 + 9 * k, -14 - 30 * k)
        val col = Zee.copy(alpha = cat.asleep * min(k * 5, 1f) * (1 - k))
        // Z's read left to right whichever way she faces.
        val o = d.m(at)
        val w = d.w(s)
        d.c.line(Vec2(o.x - w / 2, o.y - w / 2), Vec2(o.x + w / 2, o.y - w / 2), d.w(0.9f), col)
        d.c.line(Vec2(o.x + w / 2, o.y - w / 2), Vec2(o.x - w / 2, o.y + w / 2), d.w(0.9f), col)
        d.c.line(Vec2(o.x - w / 2, o.y + w / 2), Vec2(o.x + w / 2, o.y + w / 2), d.w(0.9f), col)
    }
}

/** Draws her, mapping pose units to the canvas by floor + p * size, where [floor] is the middle of the floor. [t] moves her Z's along. */
fun drawCat(c: Shapes, cat: Cat, t: Double, floor: Vec2, size: Float) {
    val d = Painter(c, floor, cat.x, cat.facing, size)
    // Turning round, her head comes round first, towards the viewer, and
    // her hips follow.
    d.headTurn = Pi * smooth(cat.turn * 1.4f)
    d.hipsTurn = Pi * smooth(cat.turn * 1.4f - 0.4f)
    val across = abs(cos((d.headTurn + d.hipsTurn) / 2))
    d.c.ellipse(d.m(Vec2(0f, 0.5f)), d.w(26 * max(0.55f, across)), d.w(2.6f + 0.8f * (1 - across)), 0f, Shadow)
    silhouette(d, cat.pose, 1.1f, Rim)
    silhouette(d, cat.pose, 0f, Fur)
    face(d, cat.pose)
    zs(d, cat, t)
}
