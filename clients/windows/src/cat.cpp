#include "cat.h"

#include <algorithm>
#include <cmath>

namespace ks {

namespace {

constexpr float kPi = 3.14159265f;

Vec2f add(Vec2f a, Vec2f b) { return {a.x + b.x, a.y + b.y}; }
Vec2f sub(Vec2f a, Vec2f b) { return {a.x - b.x, a.y - b.y}; }
Vec2f scale(Vec2f a, float s) { return {a.x * s, a.y * s}; }
Vec2f lerp(Vec2f a, Vec2f b, float k) { return {a.x + (b.x - a.x) * k, a.y + (b.y - a.y) * k}; }
float lerpf(float a, float b, float k) { return a + (b - a) * k; }
float smooth(float k) {
    k = std::clamp(k, 0.0f, 1.0f);
    return k * k * (3 - 2 * k);
}
Vec2f rotate(Vec2f v, float a) { return {v.x * std::cos(a) - v.y * std::sin(a), v.x * std::sin(a) + v.y * std::cos(a)}; }

CatPose make(Vec2f chest, float rc, Vec2f mid, float rm, Vec2f hips, float rh, Vec2f head, float tilt,
             std::array<Vec2f, 4> joint, std::array<Vec2f, 4> paw, std::array<Vec2f, CatPose::kTailPoints> tail,
             float eyes, float yawn) {
    CatPose p;
    p.chest = chest, p.rChest = rc, p.mid = mid, p.rMid = rm, p.hips = hips, p.rHips = rh;
    p.head = head, p.rHead = 8, p.headTilt = tilt;
    p.joint = joint, p.paw = paw, p.tail = tail;
    p.eyes = eyes, p.yawn = yawn;
    return p;
}

// Her poses, facing right.
const CatPose kStand = make({13, -22}, 9.5f, {0, -23}, 9, {-13, -22}, 10, {25, -31}, 0,
                            {{{14, -16}, {11, -16}, {-13, -15}, {-16, -15}}}, {{{15, 0}, {11, 0}, {-12, 0}, {-17, 0}}},
                            {{{-22, -25}, {-27, -29}, {-30, -35}, {-31, -42}, {-30, -48}, {-27, -53}, {-23, -55}, {-20, -54}}},
                            1, 0);
// Curled up asleep: head down on her paws, tail wrapped round the front.
const CatPose kSleep = make({6, -10}, 10, {-4, -12}, 11.5f, {-14, -10}, 10.5f, {19, -7.5f}, 0.25f,
                            {{{10, -4}, {8, -4}, {-12, -4}, {-15, -4}}}, {{{15, -1}, {12, -1}, {-8, -1}, {-11, -1}}},
                            {{{-23, -9}, {-25, -4}, {-21, -1}, {-13, 0}, {-4, 0}, {4, 0}, {11, -1}, {16, -2}}}, 0, 0);
// The front stretch: chest down, paws reaching, bottom and tail up, yawning.
const CatPose kBow = make({16, -11}, 8.5f, {3, -17}, 8.5f, {-12, -25}, 9.5f, {29, -14}, -0.25f,
                          {{{17, -7}, {15, -7}, {-12, -18}, {-15, -18}}}, {{{32, 0}, {29, 0}, {-11, 0}, {-15, 0}}},
                          {{{-21, -28}, {-25, -34}, {-27, -41}, {-27, -48}, {-25, -54}, {-22, -58}, {-18, -60}, {-15, -59}}},
                          0, 1);
// The arch: back up, legs together.
const CatPose kArch = make({12, -26}, 8.5f, {0, -33}, 9, {-12, -26}, 9.5f, {22, -30}, 0.3f,
                           {{{12, -19}, {10, -19}, {-11, -19}, {-13, -19}}}, {{{8, 0}, {6, 0}, {-7, 0}, {-9, 0}}},
                           {{{-20, -29}, {-24, -35}, {-26, -42}, {-26, -48}, {-24, -54}, {-21, -58}, {-17, -60}, {-14, -59}}},
                           0.6f, 0);

CatPose blend(const CatPose& a, const CatPose& b, float k) {
    CatPose p;
    p.chest = lerp(a.chest, b.chest, k), p.mid = lerp(a.mid, b.mid, k), p.hips = lerp(a.hips, b.hips, k);
    p.rChest = lerpf(a.rChest, b.rChest, k), p.rMid = lerpf(a.rMid, b.rMid, k), p.rHips = lerpf(a.rHips, b.rHips, k);
    p.head = lerp(a.head, b.head, k), p.rHead = lerpf(a.rHead, b.rHead, k), p.headTilt = lerpf(a.headTilt, b.headTilt, k);
    for (int i = 0; i < 4; i++) p.joint[i] = lerp(a.joint[i], b.joint[i], k), p.paw[i] = lerp(a.paw[i], b.paw[i], k);
    for (int i = 0; i < CatPose::kTailPoints; i++) p.tail[i] = lerp(a.tail[i], b.tail[i], k);
    p.eyes = lerpf(a.eyes, b.eyes, k), p.yawn = lerpf(a.yawn, b.yawn, k);
    return p;
}

uint32_t hash(uint32_t x) {
    x ^= x >> 16;
    x *= 0x7feb352d;
    x ^= x >> 15;
    x *= 0x846ca68b;
    x ^= x >> 16;
    return x;
}

// spot is where she settles at the end of episode k, from -1 to 1 across
// the floor.
float spot(long long k) { return float(hash(uint32_t(k) * 2654435761u) % 2001) / 1000.0f - 1; }

float sign(float v) { return v < 0 ? -1.0f : 1.0f; }

// Lengths of each part of waking up, in seconds.
constexpr float kWake = 1.4f, kToBow = 0.8f, kBowHold = 1.4f, kToArch = 0.9f, kArchHold = 0.9f, kToStand = 0.7f,
                kTurn = 1.3f, kPause = 0.5f, kLieDown = 1.8f;
constexpr float kSpeed = 22;   // units a second, walking
constexpr float kStride = 20;  // units a step cycle
constexpr float kMinWalk = 10; // closer than this, she doesn't bother

// walking moves her legs, body and tail for a step cycle at phase.
void walking(CatPose& p, float phase, double t) {
    static const float offsets[4] = {0, kPi, kPi, 0};  // diagonal pairs together
    for (int i = 0; i < 4; i++) {
        float a = phase + offsets[i];
        p.paw[i].x += 6 * std::sin(a);
        p.paw[i].y -= std::max(0.0f, std::cos(a)) * 4;
    }
    float bob = 0.8f * std::sin(2 * phase);
    p.chest.y += bob, p.mid.y += bob, p.hips.y += bob, p.head.y += bob * 0.7f;
    for (int i = 0; i < CatPose::kTailPoints; i++) p.tail[i].x += std::sin(float(t) * 2.2f + i * 0.5f) * i * 0.35f;
}

// breathing rises and falls her sides, and now and then her tail tip
// twitches in her sleep.
void breathing(CatPose& p, double t, float asleep) {
    float b = std::sin(2 * kPi * float(t) / 3.6f) * asleep;
    p.rMid += 0.7f * b, p.mid.y -= 0.4f * b;
    p.rChest += 0.3f * b;
    float twitch = std::fmod(float(t), 7.3f);
    if (twitch < 0.8f) {
        float k = std::sin(twitch / 0.8f * kPi) * asleep;
        for (int i = 5; i < CatPose::kTailPoints; i++) p.tail[i].y -= k * (i - 4) * 1.5f;
    }
}

// Rest is where she settles at the end of an episode, and which way she
// faces there.
struct Rest {
    float x = 0, facing = 1;
};

// restAfter is where she is after episode k, played from the first: she
// only walks to her next spot when it's far enough, so where she is and
// which way she faces depend on every episode before. The last answer is
// kept, so each frame takes a step at most.
Rest restAfter(long long k, float room) {
    static long long lastK = -2;
    static float lastRoom = -1;
    static Rest last;
    long long from = -1;
    Rest r{spot(-1) * room, 1};
    if (room == lastRoom && lastK >= -1 && lastK <= k) from = lastK, r = last;
    for (long long j = from + 1; j <= k; j++) {
        float to = spot(j) * room;
        if (std::fabs(to - r.x) >= kMinWalk) r.facing = sign(to - r.x), r.x = to;
    }
    lastK = k, lastRoom = room, last = r;
    return r;
}

}  // namespace

Cat CatAt(double t, float half) {
    Cat c;
    float room = std::max(0.0f, half - 46);  // keep all of her on the floor, nose to tail
    // Start some way into an episode, so she's soon up for the first time.
    double s = t + kEpisode * 0.75;
    long long k = (long long)std::floor(s / kEpisode);
    float u = float(s - double(k) * kEpisode);

    Rest rest = restAfter(k - 1, room);
    float from = rest.x, to = spot(k) * room;
    float facing = rest.facing;
    float walkTo = sign(to - from);
    bool walks = std::fabs(to - from) >= kMinWalk;
    float walk = walks ? std::fabs(to - from) / kSpeed : 0;
    float turn = walks && walkTo != facing ? kTurn : 0;
    float active = kWake + kToBow + kBowHold + kToArch + kArchHold + kToStand + turn + walk + kPause + kLieDown;
    float sleep = float(kEpisode) - active;

    c.x = from, c.facing = facing;
    // Each part of the routine, in turn.
    float at = u - sleep;
    auto part = [&](float len) {
        float k = at / len;
        at -= len;
        return k;
    };
    if (at < 0) {
        c.pose = kSleep, c.asleep = 1;
    } else if (float k = part(kWake); k < 1) {
        c.pose = blend(kSleep, kStand, smooth(k)), c.asleep = 1 - smooth(k * 2);
    } else if (k = part(kToBow); k < 1) {
        c.pose = blend(kStand, kBow, smooth(k)), c.asleep = 0;
    } else if (k = part(kBowHold); k < 1) {
        c.pose = kBow, c.asleep = 0;
    } else if (k = part(kToArch); k < 1) {
        c.pose = blend(kBow, kArch, smooth(k)), c.asleep = 0;
    } else if (k = part(kArchHold); k < 1) {
        c.pose = kArch, c.asleep = 0;
    } else if (k = part(kToStand); k < 1) {
        c.pose = blend(kArch, kStand, smooth(k)), c.asleep = 0;
    } else if (turn > 0 && (k = part(turn)) < 1) {
        // Turning round, towards the viewer: see DrawCat.
        c.pose = kStand, c.asleep = 0, c.turn = k;
        walking(c.pose, 2 * kPi * k, t);  // stepping round
    } else if (walk > 0 && (k = part(walk)) < 1) {
        c.pose = kStand, c.asleep = 0, c.facing = walkTo;
        float gone = std::fabs(to - from) * k;
        c.x = from + walkTo * gone;
        walking(c.pose, 2 * kPi * gone / kStride, t);
    } else {
        if (walks) c.x = to, c.facing = walkTo;
        if (k = part(kPause); k < 1) {
            c.pose = kStand, c.asleep = 0;
        } else {
            k = std::min(part(kLieDown), 1.0f);
            c.pose = blend(kStand, kSleep, smooth(k)), c.asleep = smooth(k * 1.5f - 0.5f);
        }
    }
    // A blink every few seconds while she's awake.
    if (std::fmod(t, 4.1) < 0.14) c.pose.eyes = 0;
    breathing(c.pose, t, c.asleep);
    return c;
}

namespace {

constexpr Rgba kFur{17, 17, 21}, kRim{92, 98, 112, 170}, kEye{206, 216, 84}, kPupil{8, 8, 10},
    kLid{120, 124, 136, 220}, kMouth{176, 84, 100}, kWhisker{150, 154, 166, 110}, kZ{150, 156, 172},
    kShadow{0, 0, 0, 55};

// Painter maps her pose onto the canvas, mirrored for which way she
// faces, and turned round about the vertical: pose x is how far forward a
// point is, and l how far to her side nearer the viewer. While she turns
// round, her head leads and her hips follow, so each point turns by an
// angle between theirs, from how far forward it is.
struct Painter {
    Canvas& c;
    Vec2f floor;
    float x, facing, size;
    float headTurn = 0, hipsTurn = 0;  // radians, 0 to pi

    float angle(float f) const { return lerpf(hipsTurn, headTurn, std::clamp((f + 13) / 38, 0.0f, 1.0f)); }
    Vec2f m(Vec2f p, float l = 0) const {
        float a = angle(p.x);
        return {floor.x + (x + facing * (p.x * std::cos(a) - l * std::sin(a))) * size, floor.y + p.y * size};
    }
    // depth is how far towards the viewer a point is, beside a centre at
    // forward f0.
    float depth(float f, float l, float f0) const {
        float a = angle(f0);
        return (f - f0) * std::sin(a) + l * std::cos(a);
    }
    float w(float r) const { return r * size; }
    void circle(Vec2f p, float r, Rgba col, float l = 0) const { c.Circle(m(p, l), w(r), col); }
    void line(Vec2f a, Vec2f b, float t, Rgba col, float l = 0) const { c.Line(m(a, l), m(b, l), w(t), col); }
    void limb(Vec2f a, Vec2f b, float t, Rgba col, float l = 0) const {
        line(a, b, t, col, l);
        circle(a, t / 2, col, l);
        circle(b, t / 2, col, l);
    }
    void poly(std::initializer_list<Vec2f> pts, Rgba col, float l = 0) const {
        Vec2f out[8];
        int n = 0;
        for (Vec2f p : pts) out[n++] = m(p, l);
        // Mirroring reverses the winding, which ImGui doesn't mind.
        c.Polygon(out, n, col);
    }
};

// silhouette draws her body, legs, tail and head in one colour, grown by
// g units all round: once wide for her rim, then in black over it.
void silhouette(const Painter& d, const CatPose& p, float g, Rgba col) {
    // Far legs, then the tail, behind the body.
    // One colour throughout, so what's in front of what doesn't matter.
    d.limb(p.joint[1], p.paw[1], 4.6f + 2 * g, col, -3.5f);
    d.limb(p.joint[3], p.paw[3], 5.2f + 2 * g, col, -4);
    for (int i = 0; i + 1 < CatPose::kTailPoints; i++) {
        float t = 4.4f - 1.4f * float(i) / (CatPose::kTailPoints - 1);
        d.limb(p.tail[i], p.tail[i + 1], t + 2 * g, col);
    }
    // The body: three circles and the quads joining them.
    auto join = [&](Vec2f a, float ra, Vec2f b, float rb) {
        Vec2f dir = sub(b, a);
        float len = std::max(0.001f, std::hypot(dir.x, dir.y));
        Vec2f n = {-dir.y / len, dir.x / len};
        d.poly({add(a, scale(n, ra + g)), add(b, scale(n, rb + g)), sub(b, scale(n, rb + g)), sub(a, scale(n, ra + g))}, col);
    };
    d.circle(p.chest, p.rChest + g, col);
    d.circle(p.mid, p.rMid + g, col);
    d.circle(p.hips, p.rHips + g, col);
    join(p.chest, p.rChest, p.mid, p.rMid);
    join(p.mid, p.rMid, p.hips, p.rHips);
    d.limb(p.joint[0], p.paw[0], 4.6f + 2 * g, col, 3.5f);
    d.limb(p.joint[2], p.paw[2], 5.2f + 2 * g, col, 4);
    // Neck, head, muzzle and ears.
    d.limb(lerp(p.chest, p.head, 0.35f), lerp(p.chest, p.head, 0.8f), 8 + 2 * g, col);
    auto head = [&](Vec2f v) { return add(p.head, rotate(v, p.headTilt)); };
    d.circle(p.head, p.rHead + g, col);
    d.circle(head({6, 2.5f}), 4.6f + g, col);
    for (float e : {-3.5f, 3.0f}) {
        float l = e < 0 ? -3.4f : 3.4f;  // side by side, seen face on
        Vec2f base1 = head({e - 3.6f, -5}), base2 = head({e + 3.6f, -5}), tip = head({e + 1.2f, -13.5f});
        Vec2f mid = scale(add(add(base1, base2), tip), 1.0f / 3);
        auto grow = [&](Vec2f v) {
            Vec2f off = sub(v, mid);
            float len = std::max(0.001f, std::hypot(off.x, off.y));
            return add(v, scale(off, g / len * 1.4f));
        };
        d.poly({grow(base1), grow(tip), grow(base2)}, col, l);
    }
}

void face(const Painter& d, const CatPose& p) {
    auto head = [&](Vec2f v) { return add(p.head, rotate(v, p.headTilt)); };
    // Both eyes, the far one hidden behind her head unless she's turned
    // towards the viewer, and narrower the more side on each is.
    for (float l : {2.6f, -2.6f}) {
        Vec2f eye = head({3.8f, -1.2f});
        float k = std::clamp((d.depth(eye.x, l, p.head.x) + 0.5f) / 3, 0.0f, 1.0f);
        if (k <= 0) continue;
        if (p.eyes > 0.15f) {
            d.c.Ellipse(d.m(eye, l), d.w(1.9f * (0.5f + 0.5f * k)), d.w(2.4f * p.eyes), 0, kEye);
            d.c.Ellipse(d.m(eye, l), d.w(0.55f), d.w(2.0f * p.eyes), 0, kPupil);
        } else {
            // Closed: a sleepy curve.
            d.line(head({2.2f, -0.6f}), head({3.8f, 0.2f}), 0.8f, kLid, l);
            d.line(head({3.8f, 0.2f}), head({5.4f, -0.6f}), 0.8f, kLid, l);
        }
    }
    if (p.yawn > 0.3f) d.c.Ellipse(d.m(head({8.5f, 4.6f})), d.w(1.4f), d.w(2.4f * p.yawn), 0, kMouth);
    // Whiskers both sides, fanning out, the far ones only once she's
    // turned enough to show them.
    bool far = std::sin(d.angle(p.head.x)) > 0.25f;
    for (float side : {1.0f, -1.0f}) {
        if (side < 0 && !far) continue;
        for (float dy : {-0.6f, 1.0f}) {
            Vec2f a = head({9, 2 + dy}), b = head({15, 1 + dy * 2.2f});
            d.c.Line(d.m(a, side * 1.5f), d.m(b, side * 6), d.w(0.45f), kWhisker);
        }
    }
}

// zs float up from her head while she's asleep: a small z, rising, growing
// and fading, one after another.
void zs(const Painter& d, const Cat& cat, double t) {
    if (cat.asleep < 0.5f) return;  // not once her head is up
    for (int i = 0; i < 3; i++) {
        float k = float(std::fmod(t / 4.5 + i / 3.0, 1.0));
        float s = 2.2f + 2.6f * k;
        Vec2f at = add(cat.pose.head, {4 + 9 * k, -14 - 30 * k});
        Rgba col = kZ;
        col.a = uint8_t(255 * cat.asleep * std::min(k * 5, 1.0f) * (1 - k));
        // Z's read left to right whichever way she faces.
        Vec2f o = d.m(at);
        float w = d.w(s);
        d.c.Line({o.x - w / 2, o.y - w / 2}, {o.x + w / 2, o.y - w / 2}, d.w(0.9f), col);
        d.c.Line({o.x + w / 2, o.y - w / 2}, {o.x - w / 2, o.y + w / 2}, d.w(0.9f), col);
        d.c.Line({o.x - w / 2, o.y + w / 2}, {o.x + w / 2, o.y + w / 2}, d.w(0.9f), col);
    }
}

}  // namespace

void DrawCat(Canvas& c, const Cat& cat, double t, Vec2f floor, float size) {
    Painter d{c, floor, cat.x, cat.facing, size};
    // Turning round, her head comes round first, towards the viewer, and
    // her hips follow.
    d.headTurn = kPi * smooth(cat.turn * 1.4f);
    d.hipsTurn = kPi * smooth(cat.turn * 1.4f - 0.4f);
    float across = std::fabs(std::cos((d.headTurn + d.hipsTurn) / 2));
    d.c.Ellipse(d.m({0, 0.5f}), d.w(26 * std::max(0.55f, across)), d.w(2.6f + 0.8f * (1 - across)), 0, kShadow);
    silhouette(d, cat.pose, 1.1f, kRim);
    silhouette(d, cat.pose, 0, kFur);
    face(d, cat.pose);
    zs(d, cat, t);
}

}  // namespace ks
