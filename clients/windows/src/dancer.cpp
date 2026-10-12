#include "dancer.h"

#include <algorithm>
#include <cmath>

namespace ks {

namespace {

constexpr float kPi = 3.14159265f;

// Proportions: a chibi's big head on a small body.
constexpr float kLeg = 34;       // hip joint to floor, standing
constexpr float kTorso = 30;     // hip to neck
constexpr float kNeckToHead = 20;
constexpr float kUpperArm = 13, kForearm = 13;
constexpr float kTailLength = 92;

Vec2f add(Vec2f a, Vec2f b) { return {a.x + b.x, a.y + b.y}; }
Vec2f scale(Vec2f a, float s) { return {a.x * s, a.y * s}; }
Vec2f lerp(Vec2f a, Vec2f b, float k) { return {a.x + (b.x - a.x) * k, a.y + (b.y - a.y) * k}; }
float lerpf(float a, float b, float k) { return a + (b - a) * k; }

// along is a vector of length len at angle a from straight down, turning
// towards side (-1 for her right, the viewer's left; +1 for her left).
Vec2f along(float a, float len, float side) { return {side * std::sin(a) * len, std::cos(a) * len}; }

// Moves is one routine's choices at a beat: arm angles (from straight
// down, outwards positive), elbow bends, the body's sway and hop, and which
// foot is lifted.
struct Moves {
    float upper[2] = {0.25f, 0.25f};
    float bend[2] = {0.3f, 0.3f};
    float sway = 0;  // sideways, units
    float lean = 0;  // radians, clockwise
    float hop = 0;   // upwards, units
    float lift[2] = {0, 0};
    float leek = -1;  // leek angle; below 0 for none
};

// mod is a remainder that's never negative: the twin-tails look back to
// before the start.
int mod(long long a, int n) { return int(((a % n) + n) % n); }

// bounce is 1 on the beat, easing to 0 between beats.
float bounce(double beat) {
    float p = float(beat - std::floor(beat));
    return 0.5f + 0.5f * std::cos(2 * kPi * p);
}

Moves routine(int r, double beat) {
    Moves m;
    float s = std::sin(kPi * float(beat));         // a side each beat
    float s2 = std::sin(kPi * float(beat) / 2);    // a side every two beats
    int side = mod((long long)std::floor(beat), 2);  // which side this beat
    switch (r) {
    case 0:  // Side steps, punching up on the side she steps to.
        m.sway = 10 * s;
        m.lean = 0.06f * s;
        m.upper[side] = 2.7f, m.bend[side] = 0.15f;
        m.upper[1 - side] = 0.35f, m.bend[1 - side] = 1.4f;
        m.lift[1 - side] = 7 * std::fabs(s);
        break;
    case 1:  // Both arms up, waving side to side.
        m.sway = 5 * s2;
        m.lean = 0.08f * s2;
        m.upper[0] = 2.6f - 0.35f * s2, m.bend[0] = 0.5f + 0.3f * s2;
        m.upper[1] = 2.6f + 0.35f * s2, m.bend[1] = 0.5f - 0.3f * s2;
        break;
    case 2:  // Ievan Polkka: twirling a leek, the other hand on her hip.
        m.sway = 6 * s;
        m.upper[0] = 0.8f, m.bend[0] = -2.2f;
        m.upper[1] = 1.9f + 0.4f * std::sin(kPi * float(beat)), m.bend[1] = 0.2f;
        m.leek = 2 * kPi * float(beat) / 2;
        m.lift[side] = 6 * std::fabs(s);
        break;
    default:  // Hops every other beat, arms flung out.
        float p = float(beat / 2 - std::floor(beat / 2));
        m.hop = p < 0.5f ? 12 * std::sin(2 * kPi * p) : 0;
        float out = m.hop / 12;
        m.upper[0] = m.upper[1] = 0.4f + 1.5f * out;
        m.bend[0] = m.bend[1] = 0.6f - 0.4f * out;
        m.lift[0] = m.lift[1] = m.hop > 0 ? 4 * out : 0;
        break;
    }
    return m;
}

Moves blend(const Moves& a, const Moves& b, float k) {
    Moves m;
    for (int i = 0; i < 2; i++) {
        m.upper[i] = lerpf(a.upper[i], b.upper[i], k);
        m.bend[i] = lerpf(a.bend[i], b.bend[i], k);
        m.lift[i] = lerpf(a.lift[i], b.lift[i], k);
    }
    m.sway = lerpf(a.sway, b.sway, k);
    m.lean = lerpf(a.lean, b.lean, k);
    m.hop = lerpf(a.hop, b.hop, k);
    m.leek = k < 0.5f ? a.leek : b.leek;
    return m;
}

constexpr int kRoutines = 4, kBeatsEach = 8;

// movesAt is the moves at t, blending into the next routine over its last
// beat.
Moves movesAt(double t) {
    double beat = t * kBpm / 60;
    int r = mod((long long)std::floor(beat / kBeatsEach), kRoutines);
    double into = beat - std::floor(beat / kBeatsEach) * kBeatsEach;
    Moves m = routine(r, beat);
    if (into > kBeatsEach - 1) {
        float k = float(into - (kBeatsEach - 1));
        k = k * k * (3 - 2 * k);  // smoothstep
        m = blend(m, routine((r + 1) % kRoutines, beat), k);
    }
    return m;
}

// headAt is where her head is at t, for the twin-tails to trail behind.
Vec2f headAt(double t) {
    Moves m = movesAt(t);
    float dip = 4 * bounce(t * kBpm / 60) * (m.hop > 0 ? 0 : 1);
    Vec2f hip{m.sway, -kLeg + dip - m.hop};
    return add(hip, along(kPi + m.lean, kTorso + kNeckToHead, 1));
}

uint32_t hash(uint32_t x) {
    x ^= x >> 16;
    x *= 0x7feb352d;
    x ^= x >> 15;
    x *= 0x846ca68b;
    x ^= x >> 16;
    return x;
}

}  // namespace

Pose Dance(double t) {
    Pose p;
    double beat = t * kBpm / 60;
    Moves m = movesAt(t);
    // Knees give on the beat, except mid-hop.
    float dip = 4 * bounce(beat) * (m.hop > 0 ? 0 : 1);

    p.hip = {m.sway, -kLeg + dip - m.hop};
    p.neck = add(p.hip, along(kPi + m.lean, kTorso, 1));
    p.headTilt = m.lean * 1.6f;
    p.head = add(p.neck, along(kPi + p.headTilt, kNeckToHead, 1));

    for (int i = 0; i < 2; i++) {
        float side = i == 0 ? -1.0f : 1.0f;
        // The shoulders and hips follow the lean.
        Vec2f across{std::cos(m.lean), std::sin(m.lean)};
        p.shoulder[i] = add(add(p.neck, scale(across, side * 9)), along(m.lean, 3, 1));
        p.elbow[i] = add(p.shoulder[i], along(m.upper[i], kUpperArm, side));
        p.hand[i] = add(p.elbow[i], along(m.upper[i] + m.bend[i], kForearm, side));
        p.hipJoint[i] = add(p.hip, scale(across, side * 6));
        // Feet stay planted under the hips' rest position unless lifted.
        p.foot[i] = {side * 8 + m.sway * 0.4f, -m.lift[i] - m.hop};
        Vec2f mid = lerp(p.hipJoint[i], p.foot[i], 0.5f);
        float bendOut = (dip + m.lift[i]) * 0.8f;
        p.knee[i] = {mid.x + side * bendOut, mid.y};
    }

    // Each tail is a chain whose points are where it was a moment ago,
    // relative to her head now, so it swings after her and settles.
    Vec2f now = headAt(t);
    for (int i = 0; i < 2; i++) {
        float side = i == 0 ? -1.0f : 1.0f;
        Vec2f root = add(p.head, {side * 17, -9});
        for (int k = 0; k < Pose::kTailPoints; k++) {
            float f = float(k) / (Pose::kTailPoints - 1);
            Vec2f past = headAt(t - 0.045 * k);
            Vec2f trail = {(past.x - now.x) * 1.6f, (past.y - now.y) * 0.6f};
            // Out from the head, then hanging down.
            Vec2f rest = {side * (6 + 17 * std::sin(f * kPi * 0.75f)), f * kTailLength};
            p.tails[i][k] = add(add(root, rest), trail);
            // Hair rests on the floor rather than going through it.
            p.tails[i][k].y = std::min(p.tails[i][k].y, -3.0f);
        }
    }

    if (m.leek >= 0) p.leek = true, p.leekAngle = m.leek;
    // Singing: the mouth opens with the beat.
    p.mouth = std::clamp(bounce(beat * 2) * 1.2f - 0.2f, 0.0f, 1.0f);
    // A blink every few seconds.
    p.blink = std::fmod(t, 3.7) < 0.12;
    return p;
}

std::array<Note, kMaxNotes> Notes(double t) {
    std::array<Note, kMaxNotes> out{};
    constexpr double every = 0.7, life = every * kMaxNotes;
    long long newest = (long long)std::floor(t / every);
    for (int i = 0; i < kMaxNotes; i++) {
        long long n = newest - i;
        float age = float((t - double(n) * every) / life);  // 0 to 1
        uint32_t h = hash(uint32_t(n));
        float side = (h & 1) ? 1.0f : -1.0f;
        float x0 = side * (26 + float(h >> 8 & 15));
        out[i].at = {x0 + side * 14 * age + 5 * std::sin(age * 9 + float(h & 7)), -62 - 46 * age};
        out[i].alpha = std::clamp(std::min(age * 6, (1 - age) * 2.5f), 0.0f, 1.0f);
        out[i].pink = (h >> 4) & 1;
    }
    return out;
}

namespace {

// Her colours.
constexpr Rgba kHair{57, 197, 187}, kHairShade{31, 150, 148}, kSkin{255, 228, 212}, kTop{205, 210, 216},
    kDark{43, 46, 54}, kTie{57, 197, 187}, kPink{225, 40, 133}, kEye{24, 140, 150}, kMouth{190, 70, 90},
    kLeek{106, 170, 70}, kLeekWhite{240, 244, 230}, kShadow{0, 0, 0, 50};

// Painter maps pose units onto the canvas.
struct Painter {
    Canvas& c;
    Vec2f at;
    float size;
    Vec2f m(Vec2f p) const { return {at.x + p.x * size, at.y + p.y * size}; }
    void circle(Vec2f p, float r, Rgba col) const { c.Circle(m(p), r * size, col); }
    void ellipse(Vec2f p, float rx, float ry, float rot, Rgba col) const { c.Ellipse(m(p), rx * size, ry * size, rot, col); }
    // A limb: a thick line with round ends.
    void limb(Vec2f a, Vec2f b, float w, Rgba col) const {
        c.Line(m(a), m(b), w * size, col);
        circle(a, w / 2, col);
        circle(b, w / 2, col);
    }
    void poly(std::initializer_list<Vec2f> pts, Rgba col) const {
        Vec2f out[16];
        int n = 0;
        for (Vec2f p : pts) out[n++] = m(p);
        c.Polygon(out, n, col);
    }
};

Vec2f rotate(Vec2f v, float a) { return {v.x * std::cos(a) - v.y * std::sin(a), v.x * std::sin(a) + v.y * std::cos(a)}; }

void tail(const Painter& d, const std::array<Vec2f, Pose::kTailPoints>& pts) {
    for (int k = 0; k + 1 < Pose::kTailPoints; k++) {
        float f = float(k) / (Pose::kTailPoints - 1);
        float w = 13 - 8 * f;  // tapering
        d.limb(pts[k], pts[k + 1], w, k % 2 ? kHair : kHair);
    }
    // A darker strand down the middle for depth.
    for (int k = 1; k + 1 < Pose::kTailPoints; k++) d.c.Line(d.m(pts[k]), d.m(pts[k + 1]), 2.2f * d.size, kHairShade);
}

void note(const Painter& d, const Note& n) {
    if (n.alpha <= 0) return;
    Rgba col = n.pink ? kPink : kHairShade;
    col.a = uint8_t(255 * n.alpha);
    d.ellipse(n.at, 3.4f, 2.6f, -0.4f, col);
    Vec2f top = {n.at.x + 3, n.at.y - 13};
    d.c.Line(d.m({n.at.x + 3, n.at.y - 1}), d.m(top), 1.4f * d.size, col);
    d.c.Line(d.m(top), d.m({top.x + 5, top.y + 5}), 1.6f * d.size, col);
}

}  // namespace

void DrawDancer(Canvas& c, const Pose& p, const std::array<Note, kMaxNotes>& notes, Vec2f at, float size) {
    Painter d{c, at, size};
    // Her shadow shrinks as she leaves the floor.
    float air = std::clamp(-std::min(p.foot[0].y, p.foot[1].y) / 12, 0.0f, 1.0f);
    d.ellipse({p.hip.x, 1}, 24 - 6 * air, 4 - air, 0, kShadow);

    for (const Note& n : notes) note(d, n);

    // Behind her: the twin-tails and the back of her hair.
    tail(d, p.tails[0]);
    tail(d, p.tails[1]);
    d.circle(p.head, 22, kHair);

    // Legs: skin above the knee, long black boots below.
    for (int i = 0; i < 2; i++) {
        d.limb(p.hipJoint[i], p.knee[i], 5.5f, kSkin);
        d.limb(p.knee[i], p.foot[i], 6.5f, kDark);
        d.limb(lerp(p.knee[i], p.foot[i], -0.05f), lerp(p.knee[i], p.foot[i], 0.02f), 7.5f, kHair);  // boot trim
        d.ellipse(add(p.foot[i], {i == 0 ? -2.0f : 2.0f, 0}), 5, 2.5f, 0, kDark);
    }

    // Skirt, top and tie, leaning with her.
    float lean = std::atan2(p.neck.x - p.hip.x, p.hip.y - p.neck.y);
    auto body = [&](Vec2f v) { return add(p.hip, rotate(v, lean)); };
    d.poly({body({-8, -2}), body({8, -2}), body({14, 9}), body({-14, 9})}, kDark);
    d.poly({body({-8, 9}), body({8, 9}), body({10, 9.5f}), body({-10, 9.5f})}, kHair);  // hem
    d.poly({body({-9, -kTorso + 2}), body({9, -kTorso + 2}), body({8, -1}), body({-8, -1})}, kTop);
    d.poly({body({-2.5f, -kTorso + 3}), body({2.5f, -kTorso + 3}), body({1.5f, -kTorso + 15}), body({0, -kTorso + 18}),
            body({-1.5f, -kTorso + 15})},
           kTie);

    // Arms: skin to the elbow, then her black sleeves with teal cuffs.
    for (int i = 0; i < 2; i++) {
        d.limb(p.shoulder[i], p.elbow[i], 4.5f, kSkin);
        d.limb(p.elbow[i], lerp(p.elbow[i], p.hand[i], 0.8f), 7, kDark);
        d.circle(lerp(p.elbow[i], p.hand[i], 0.8f), 3.8f, kHair);
        d.circle(p.hand[i], 3, kSkin);
    }
    if (p.leek) {
        Vec2f dir = {std::sin(p.leekAngle), -std::cos(p.leekAngle)};
        Vec2f tip = add(p.hand[1], scale(dir, 20)), base = add(p.hand[1], scale(dir, -9));
        d.limb(p.hand[1], tip, 3.5f, kLeek);
        d.limb(base, p.hand[1], 4, kLeekWhite);
        d.circle(p.hand[1], 3, kSkin);
    }

    // Head: face, fringe, eyes and mouth, tilted with her.
    auto face = [&](Vec2f v) { return add(p.head, rotate(v, p.headTilt)); };
    d.circle(face({0, 2}), 18, kSkin);
    // Fringe: a cap of hair and a few points over the forehead.
    d.ellipse(face({0, -10}), 20, 10, p.headTilt, kHair);
    for (float x : {-14.0f, -6.0f, 3.0f, 11.0f})
        d.poly({face({x - 6, -8}), face({x + 5, -8}), face({x + 1, 3})}, kHair);
    // Hair ties where the tails start.
    for (int i = 0; i < 2; i++) {
        Vec2f root = p.tails[i][0];
        d.poly({add(root, {-3, -4}), add(root, {3, -4}), add(root, {3, 4}), add(root, {-3, 4})}, kDark);
        d.circle(add(root, {0, -5}), 1.6f, kPink);
    }
    for (float side : {-1.0f, 1.0f}) {
        Vec2f eye = face({side * 7.5f, 4});
        if (p.blink) {
            d.c.Line(d.m(face({side * 7.5f - 3.5f, 5})), d.m(face({side * 7.5f + 3.5f, 5})), 1.6f * size, kDark);
        } else {
            d.ellipse(eye, 3.6f, 5, p.headTilt, kEye);
            d.ellipse(face({side * 7.5f, 2.5f}), 2.2f, 3, p.headTilt, kDark);
            d.circle(face({side * 7.5f - 1.2f, 1}), 1.1f, {255, 255, 255});
        }
        d.ellipse(face({side * 12, 9}), 3, 1.6f, 0, {255, 170, 180, 120});  // blush
    }
    d.ellipse(face({0, 12}), 2.6f, 0.6f + 2.4f * p.mouth, p.headTilt, kMouth);
}

}  // namespace ks

