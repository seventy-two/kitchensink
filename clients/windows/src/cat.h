// A black cat who sleeps above the Recent column and every so often wakes,
// stretches, wanders to another spot and curls up again. Where she is and
// how she's lying at any moment is worked out here, so it can be tested
// anywhere; the app draws her through a Canvas.
#pragma once

#include <array>
#include <cstdint>

namespace ks {

struct Vec2f {
    float x = 0, y = 0;
};

// CatPose is her shape, side on, facing right, in units with the floor at
// y = 0 below her middle and y growing downwards. Her body is three
// circles, chest to hips, joined smoothly.
struct CatPose {
    Vec2f chest, mid, hips;
    float rChest = 0, rMid = 0, rHips = 0;
    Vec2f head;
    float rHead = 0;
    float headTilt = 0;  // radians, clockwise: down towards her chest
    // Legs: front near, front far, back near, back far.
    std::array<Vec2f, 4> joint, paw;
    static constexpr int kTailPoints = 8;
    std::array<Vec2f, kTailPoints> tail;  // from the base out
    float eyes = 0;  // 0 closed to 1 open
    float yawn = 0;  // 0 to 1
};

// Cat is her at one moment: her pose, where she is along the floor and
// which way she faces (1 right, -1 left, between while turning round), and
// how asleep she is (1 fast asleep, for her breathing and Z's).
struct Cat {
    CatPose pose;
    float x = 0;
    float facing = 1;
    float asleep = 1;
};

// CatAt is the cat t seconds in, with half units of floor either side of
// the middle to wander. She sleeps for most of each kEpisode seconds.
constexpr double kEpisode = 100;
Cat CatAt(double t, float half);

// Rgba is a colour, 0 to 255 each.
struct Rgba {
    uint8_t r = 0, g = 0, b = 0, a = 255;
};

// Canvas is what she's drawn on: ImGui's draw list in the app, SVG or a
// recorder in tests. Thicknesses and radii are in canvas units.
class Canvas {
public:
    virtual ~Canvas() = default;
    virtual void Circle(Vec2f c, float r, Rgba col) = 0;
    virtual void Ellipse(Vec2f c, float rx, float ry, float rot, Rgba col) = 0;
    virtual void Line(Vec2f a, Vec2f b, float thickness, Rgba col) = 0;
    virtual void Polygon(const Vec2f* points, int n, Rgba col) = 0;  // convex, filled
};

// DrawCat draws her, mapping pose units to the canvas by floor + p * size,
// where floor is the middle of the floor. t moves her Z's along.
void DrawCat(Canvas& c, const Cat& cat, double t, Vec2f floor, float size);

}  // namespace ks
