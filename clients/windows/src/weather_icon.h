// Small weather pictures, for the hourly forecast's chips: sun, moon,
// cloud, rain and so on, drawn from shapes rather than a font, so they
// take the theme's colours.
#pragma once

#include <string>

#include "canvas.h"

namespace ks {

struct IconColours {
    Rgba sun, moon, cloud, darkCloud, rain, snow, bolt;
};

// IsWeatherIcon says whether name is one DrawWeatherIcon knows: sun, moon,
// sun-cloud, moon-cloud, cloud, fog, drizzle, rain, showers, snow, storm.
bool IsWeatherIcon(const std::string& name);

// DrawWeatherIcon draws the named icon in the square at topLeft, size
// across. Unknown names draw nothing.
void DrawWeatherIcon(Canvas& c, const std::string& name, Vec2f topLeft, float size, const IconColours& col);

}  // namespace ks
