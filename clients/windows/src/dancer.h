// A chibi Miku who dances above the Recent column: the pose at any moment,
// worked out here so it can be tested anywhere, and drawn by the app.
#pragma once

#include <array>
#include <cstdint>

namespace ks {

struct Vec2f {
    float x = 0, y = 0;
};

// Pose is where everything is at one moment, in units with the stage floor
// at y = 0 below the middle and y growing downwards. She stands about 105
// units tall, a little more when she hops. Index 0 is her right (the
// viewer's left), 1 her left.
struct Pose {
    Vec2f hip, neck, head;  // head is the centre of her face
    float headTilt = 0;     // radians, clockwise
    std::array<Vec2f, 2> shoulder, elbow, hand;
    std::array<Vec2f, 2> hipJoint, knee, foot;
    // Each twin-tail as a chain from its root down, trailing her moves.
    static constexpr int kTailPoints = 7;
    std::array<std::array<Vec2f, kTailPoints>, 2> tails;
    bool leek = false;   // a spring onion in hand[1], as in Ievan Polkka
    float leekAngle = 0; // radians from pointing up, clockwise
    float mouth = 0;     // 0 closed to 1 open
    bool blink = false;
};

// Note is a musical note drifting up from her, faded by alpha (0 to 1).
struct Note {
    Vec2f at;
    float alpha = 0;
    bool pink = false;
};

// kBpm is the tempo she dances to.
constexpr double kBpm = 128;

// Dance is her pose t seconds in. She loops through four routines of eight
// beats each, blending from one to the next.
Pose Dance(double t);

// Notes are the notes in the air at t, up to kMaxNotes.
constexpr int kMaxNotes = 5;
std::array<Note, kMaxNotes> Notes(double t);

// Rgba is a colour, 0 to 255 each.
struct Rgba {
    uint8_t r = 0, g = 0, b = 0, a = 255;
};

// Canvas is what she's drawn on: ImGui's draw list in the app, SVG in
// tests. Thicknesses and radii are in the same units as points.
class Canvas {
public:
    virtual ~Canvas() = default;
    virtual void Circle(Vec2f c, float r, Rgba col) = 0;
    virtual void Ellipse(Vec2f c, float rx, float ry, float rot, Rgba col) = 0;
    virtual void Line(Vec2f a, Vec2f b, float thickness, Rgba col) = 0;
    virtual void Polygon(const Vec2f* points, int n, Rgba col) = 0;  // convex, filled
};

// DrawDancer draws her pose and the notes around her, in pose units mapped
// to the canvas by at + point * size.
void DrawDancer(Canvas& c, const Pose& p, const std::array<Note, kMaxNotes>& notes, Vec2f at, float size);

}  // namespace ks
