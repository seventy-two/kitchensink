#include "weather_icon.h"

#include <cmath>

namespace ks {

namespace {

constexpr float kPi = 3.14159265f;

// Pen draws in the icon's own units, 0 to 1 across its square, through a
// shift and scale about the middle, so a shape can be drawn smaller or to
// one side, e.g. a cloud in front of the sun.
struct Pen {
    Canvas& c;
    Vec2f at;
    float size;
    float k = 1;
    Vec2f shift{};

    Vec2f p(float x, float y) const {
        return {at.x + ((x - 0.5f) * k + 0.5f + shift.x) * size, at.y + ((y - 0.5f) * k + 0.5f + shift.y) * size};
    }
    float d(float v) const { return v * k * size; }
    void circle(float x, float y, float r, Rgba col) const { c.Circle(p(x, y), d(r), col); }
    void line(float x0, float y0, float x1, float y1, float w, Rgba col) const { c.Line(p(x0, y0), p(x1, y1), d(w), col); }
    void poly(std::initializer_list<Vec2f> pts, Rgba col) const {
        Vec2f out[8];
        int n = 0;
        for (Vec2f v : pts)
            if (n < 8) out[n++] = p(v.x, v.y);
        c.Polygon(out, n, col);
    }
};

// A cloud spans 0.15 to 0.89 across and 0.27 to 0.77 down, flat beneath.
void cloud(const Pen& pen, Rgba col) {
    pen.circle(0.32f, 0.60f, 0.17f, col);
    pen.circle(0.54f, 0.48f, 0.21f, col);
    pen.circle(0.73f, 0.61f, 0.16f, col);
    pen.poly({{0.32f, 0.55f}, {0.73f, 0.55f}, {0.73f, 0.77f}, {0.32f, 0.77f}}, col);
}

void sun(const Pen& pen, float x, float y, float r, Rgba col) {
    pen.circle(x, y, r, col);
    for (int i = 0; i < 8; i++) {
        float a = float(i) * kPi / 4, cs = std::cos(a), sn = std::sin(a);
        pen.line(x + cs * r * 1.45f, y + sn * r * 1.45f, x + cs * r * 2.05f, y + sn * r * 2.05f, r * 0.35f, col);
    }
}

// A crescent, lit on the left: slices between its outer edge, a half
// circle, and its inner one, a narrower half ellipse.
void moon(const Pen& pen, float x, float y, float r, Rgba col) {
    constexpr int n = 16;
    auto edge = [&](int i, float k) {
        float a = kPi * (0.5f + float(i) / n);
        return Vec2f{x + std::cos(a) * r * k, y + std::sin(a) * r};
    };
    for (int i = 0; i < n; i++) {
        Vec2f o0 = edge(i, 1), o1 = edge(i + 1, 1), i0 = edge(i, 0.15f), i1 = edge(i + 1, 0.15f);
        pen.poly({o0, o1, i1, i0}, col);
    }
}

// A cloud raised to leave room beneath it for rain or snow.
Pen raised(Pen pen) {
    pen.shift.y -= 0.17f;
    return pen;
}

void drops(const Pen& pen, float len, float w, Rgba col) {
    for (float x : {0.36f, 0.54f, 0.72f}) pen.line(x, 0.68f, x - len * 0.35f, 0.68f + len, w, col);
}

}  // namespace

bool IsWeatherIcon(const std::string& n) {
    for (const char* k : {"sun", "moon", "sun-cloud", "moon-cloud", "cloud", "fog", "drizzle", "rain", "showers", "snow", "storm"})
        if (n == k) return true;
    return false;
}

void DrawWeatherIcon(Canvas& c, const std::string& n, Vec2f topLeft, float size, const IconColours& col) {
    Pen pen{c, topLeft, size};
    if (n == "sun") {
        sun(pen, 0.5f, 0.5f, 0.2f, col.sun);
    } else if (n == "moon") {
        moon(pen, 0.58f, 0.5f, 0.32f, col.moon);
    } else if (n == "sun-cloud" || n == "moon-cloud") {
        if (n == "sun-cloud") sun(pen, 0.33f, 0.33f, 0.13f, col.sun);
        else moon(pen, 0.38f, 0.3f, 0.22f, col.moon);
        Pen front = pen;
        front.k = 0.85f;
        front.shift = {0.08f, 0.06f};
        cloud(front, col.cloud);
    } else if (n == "cloud") {
        cloud(pen, col.cloud);
    } else if (n == "fog") {
        pen.line(0.18f, 0.38f, 0.82f, 0.38f, 0.07f, col.cloud);
        pen.line(0.26f, 0.54f, 0.9f, 0.54f, 0.07f, col.cloud);
        pen.line(0.12f, 0.70f, 0.72f, 0.70f, 0.07f, col.cloud);
    } else if (n == "drizzle") {
        cloud(raised(pen), col.cloud);
        drops(pen, 0.12f, 0.065f, col.rain);
    } else if (n == "rain") {
        cloud(raised(pen), col.darkCloud);
        drops(pen, 0.22f, 0.065f, col.rain);
    } else if (n == "showers") {
        sun(pen, 0.68f, 0.21f, 0.09f, col.sun);
        cloud(raised(pen), col.cloud);
        drops(pen, 0.16f, 0.065f, col.rain);
    } else if (n == "snow") {
        cloud(raised(pen), col.cloud);
        for (Vec2f s : {Vec2f{0.34f, 0.72f}, Vec2f{0.54f, 0.82f}, Vec2f{0.74f, 0.72f}, Vec2f{0.44f, 0.92f}, Vec2f{0.64f, 0.92f}})
            pen.circle(s.x, s.y, 0.05f, col.snow);
    } else if (n == "storm") {
        cloud(raised(pen), col.darkCloud);
        pen.poly({{0.58f, 0.58f}, {0.42f, 0.78f}, {0.56f, 0.78f}}, col.bolt);
        pen.poly({{0.50f, 0.74f}, {0.64f, 0.74f}, {0.46f, 0.96f}}, col.bolt);
    }
}

}  // namespace ks
