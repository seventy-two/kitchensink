package ebay

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/ai"
)

// Gemini names what each listing is and rates how notable it is to a
// collector.
type Gemini struct {
	// APIKey is a Gemini API key; use "${NAME}" to keep it in the
	// environment.
	APIKey string `json:"apiKey"`
	// Model defaults to gemini-3.8-flash, until another is chosen in
	// the "ai" monitor: judging rarity needs what it
	// knows about each thing, which the lite model is weaker on.
	Model string `json:"model"`
	// URL replaces the Interactions API endpoint, for tests.
	URL string `json:"url"`
}

type gemini struct{ ai *ai.Client }

func newGemini(c Gemini) *gemini {
	return &gemini{ai.New(ai.Use{ID: "ebay", Label: "Big box", Gemini: cmp.Or(c.Model, "gemini-3.8-flash")},
		ai.GeminiConfig{APIKey: c.APIKey, URL: c.URL}, ai.WithTimeout(geminiTimeout))}
}

const instruction = `You name eBay listings for a personal dashboard. Sellers pad their titles with keywords (platform, condition, "rare", "vintage", "big box", "PC CD-ROM", "complete", years, publishers, "VGC", "tested"), so give only what's being sold, as it's properly written: its official title with the right capitals and punctuation, e.g. "Ultima VII: The Black Gate", "Half-Life", "SimCity 2000". Keep an edition or extra only when it's part of what's sold ("Collector's Edition", "with Expansion Pack"). For several items, join them with " + ", or say "N games" for a large lot. If you can't tell what it is, tidy the title down to its likely name.

Also rate how notable it is to a collector of big box PC games: "common", "uncommon" or "rare". The collector wants famous games whose boxed PC copy is hard to find, and special versions of boxes; nothing else. Judge the thing itself, from what you know of it; ignore what the seller says ("rare", "HTF", "L@@K", "grail", "vintage", "sealed", "mint") and the asking price.

"rare" is for:
- A household-name game, usually one famous on consoles, whose big box PC release sold few copies and seldom turns up, e.g. Wipeout, Rayman, Croc, Crash Bandicoot, Final Fantasy VII, Metal Gear Solid.
- A special version of a box: a shadowbox (e.g. Unreal's), a tin, a limited, numbered or collector's edition, an unusually shaped or oversized box, a pre-release or promotional copy.

"uncommon" is the same kind of thing, a little less striking: a well-known game, often one famous on consoles or from a big series, whose PC big box doesn't turn up often, e.g. Little Big Adventure.

"common" is everything else, which is nearly every listing, including:
- PC games of the DOS and early Windows era, however old, classic or loved: their big boxes turn up all the time. Sierra and LucasArts adventures, Ultima, Origin, MicroProse, Westwood, Bullfrog, Apogee and id shooters, Doom, Quake, Duke Nukem 3D, Rise of the Triad, Simon the Sorcerer, Myst, Half-Life, Tomb Raider, Carmageddon, Worms, the standard edition of Unreal.
- Expansion packs, mission packs, add-ons, compilations, bundles, budget re-releases and "Gold" or "Game of the Year" editions.
- Versions for anything but the PC (Apple II, Amiga, Atari ST, C64, Mac).
- Obscure games few people have heard of, however scarce.
- Sports, flight sims, edutainment and licensed kids' games.
- Anything released after about 2004, when big boxes had given way to DVD cases, collector's and limited editions included: they were made for collectors and kept boxed.
Being sealed or new never makes a listing more notable. If you're unsure, say "common".`

var schema = map[string]any{
	"type": "array",
	"items": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":     map[string]any{"type": "string"},
			"name":   map[string]any{"type": "string"},
			"rarity": map[string]any{"type": "string", "enum": rarities},
		},
		"required": []string{"id", "name", "rarity"},
	},
}

// geminiTimeout allows for a slow reply naming a page of listings.
const geminiTimeout = 90 * time.Second

// rarities are how rare Gemini can say a listing is, least first.
var rarities = []string{"common", "uncommon", "rare"}

// rank places a rarity in rarities, or -1 for none or one not known.
func rank(r string) int {
	for i, x := range rarities {
		if x == r {
			return i
		}
	}
	return -1
}

// judged is what Gemini said about a listing.
type judged struct{ Name, Rarity string }

// judge asks Gemini what each listing is and how rare, by item ID. what says
// what kind of thing they are, e.g. "a boxed PC game".
func (g *gemini) judge(ctx context.Context, client *http.Client, what string, items []item) (map[string]judged, error) {
	type in struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	var list []in
	for _, it := range items {
		list = append(list, in{it.ItemID, it.Title})
	}
	input, _ := json.Marshal(list)
	sys := instruction
	if what != "" {
		sys += "\n\nEach listing is " + what + "."
	}
	var got []struct{ ID, Name, Rarity string }
	if err := g.ai.Ask(ctx, client, ai.Request{
		System:    sys,
		Text:      "Name each listing and say how rare it is, by its id:\n" + string(input),
		Schema:    schema,
		MaxTokens: 8000,
	}, &got); err != nil {
		return nil, err
	}
	judgements := map[string]judged{}
	asked := map[string]bool{}
	for _, it := range items {
		asked[it.ItemID] = true
	}
	for _, g := range got {
		if n := strings.TrimSpace(g.Name); n != "" && asked[g.ID] {
			j := judged{Name: n}
			if rank(g.Rarity) >= 0 {
				j.Rarity = g.Rarity
			}
			judgements[g.ID] = j
		}
	}
	return judgements, nil
}
