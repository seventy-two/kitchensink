// Canvas is something to draw simple shapes on: ImGui's draw list in the
// app, a recorder or SVG in tests. The cat and the weather icons draw
// through it, so they can be tested anywhere.
#pragma once

#include <cstdint>

namespace ks {

struct Vec2f {
    float x = 0, y = 0;
};

// Rgba is a colour, 0 to 255 each.
struct Rgba {
    uint8_t r = 0, g = 0, b = 0, a = 255;
};

// Thicknesses and radii are in canvas units.
class Canvas {
public:
    virtual ~Canvas() = default;
    virtual void Circle(Vec2f c, float r, Rgba col) = 0;
    virtual void Ellipse(Vec2f c, float rx, float ry, float rot, Rgba col) = 0;
    virtual void Line(Vec2f a, Vec2f b, float thickness, Rgba col) = 0;
    virtual void Polygon(const Vec2f* points, int n, Rgba col) = 0;  // convex, filled
};

}  // namespace ks
