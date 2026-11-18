package ebay

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"slices"
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

// grail is the tier above rare: a game on the config's Grails list,
// never given by the model.
const grail = "grail"

// tiers are rarities and grail, least first.
var tiers = append(slices.Clone(rarities), grail)

// rank places a tier, or -1 for none or one not known.
func rank(r string) int {
	for i, x := range tiers {
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
func (g *gemini) judge(ctx context.Context, client *http.Client, kind, what string, items []item) (map[string]judged, error) {
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
	if kind == kindGPUs {
		sys = gpuInstruction
	}
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

// dealInstruction asks whether asking prices are fair.
const dealInstruction = `You judge eBay listings of boxed PC games for a collector in the UK: is the asking price (postage included) a good deal, fair, or overpriced, against what such items actually sell for on eBay UK today (not what sellers ask)? Go by the game as described (complete, sealed, disc only, manual only, a lot of several) in the condition the title suggests, used and in good order if it doesn't say.

For each give:
- verdict: "good" if it's clearly below what it sells for, "overpriced" if clearly above, else "fair".
- low, high: the fair range in whole pounds, postage included.
- why: a few words, e.g. "sells for £25-35 complete".`

var dealSchema = map[string]any{
	"type": "array",
	"items": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":      map[string]any{"type": "string"},
			"verdict": map[string]any{"type": "string", "enum": []string{"good", "fair", "overpriced"}},
			"low":     map[string]any{"type": "number"},
			"high":    map[string]any{"type": "number"},
			"why":     map[string]any{"type": "string"},
		},
		"required": []string{"id", "verdict", "low", "high", "why"},
	},
}

// deal is what an asking price is: good, fair or overpriced against the
// fair range Low to High (pounds, postage included), and why. Asking is
// the price it was judged at; Yours says the range came from the config's
// Prices rather than the model.
type deal struct {
	ID      string  `json:"id,omitempty"`
	Verdict string  `json:"verdict"`
	Low     float64 `json:"low"`
	High    float64 `json:"high"`
	Why     string  `json:"why,omitempty"`
	Asking  float64 `json:"asking,omitempty"`
	Yours   bool    `json:"yours,omitempty"`
}

// dealBatch is how many listings are judged in one request: few, so each
// gets looked up.
const dealBatch = 8

// deals asks whether each listing's asking price (pounds, postage
// included, by item ID) is a good deal, with web search for what such
// things sell for. what says what kind of thing they are.
func (g *gemini) deals(ctx context.Context, client *http.Client, kind, what string, items []item, asking map[string]float64) (map[string]deal, error) {
	type in struct {
		ID     string  `json:"id"`
		Title  string  `json:"title"`
		Asking float64 `json:"asking"`
	}
	var list []in
	for _, it := range items {
		list = append(list, in{it.ItemID, it.Title, asking[it.ItemID]})
	}
	input, _ := json.Marshal(list)
	sys := dealInstruction
	if kind == kindGPUs {
		sys = gpuDealInstruction
	}
	if what != "" {
		sys += "\n\nEach listing is " + what + "."
	}
	var got []deal
	if err := g.ai.Ask(ctx, client, ai.Request{
		System:    sys,
		Text:      "Judge each listing's asking price (in pounds, postage included), by its id:\n" + string(input),
		Schema:    dealSchema,
		MaxTokens: 8000,
		Search:    true,
	}, &got); err != nil {
		return nil, err
	}
	out := map[string]deal{}
	for _, d := range got {
		if _, ok := asking[d.ID]; ok && rankDeal(d.Verdict) && d.High >= d.Low {
			d.Asking, d.Why = asking[d.ID], strings.TrimSpace(d.Why)
			out[d.ID] = d
		}
	}
	return out, nil
}

// rankDeal says whether v is a verdict the model may give.
func rankDeal(v string) bool { return v == "good" || v == "fair" || v == "overpriced" }

// Kinds of thing a section lists, which say how the model names and rates
// them.
const (
	kindGames = "games"
	kindGPUs  = "gpus"
)

// gpuInstruction names and rates graphics cards for a collector of iconic
// ones up to the RTX 2080's generation.
const gpuInstruction = `You name eBay listings of graphics cards for a collector's dashboard. Sellers pad their titles with keywords (board maker, memory, slot, ports, "rare", "retro", "vintage", "tested", "working"), so give only the card, by its chip's model name as it's properly written, without the board maker or "NVIDIA", "ATI" or "AMD" in front: e.g. "GeForce 8800 GTX", "Radeon HD 5870", "Voodoo5 5500", "GeForce4 Ti 4600", "Radeon 9700 Pro", "GeForce GTX 1080 Ti", "GeForce RTX 2080 Super". Keep a special edition only when it's part of what's sold ("Founders Edition", "XXX Edition"). For several cards join them with " + ", or say "N graphics cards" for a large lot. If it isn't a graphics card (a cooler, a box, a bracket, a whole PC), say briefly what it is.

Then say how notable it is to a collector of iconic graphics cards from before 2019 (up to the RTX 2080's generation): how seldom such a card turns up, from what you know of the card itself, not the seller's "RARE" or the price. Being famous isn't enough: cards that sold in their millions and are still used for gaming turn up every day.

"rare" is a card collectors seek that seldom turns up:
- 3dfx's Voodoo4 and Voodoo5 cards and Voodoo2 SLI pairs, GeForce 256, GeForce2 Ultra, GeForce3 Ti 500, GeForce4 Ti 4600 and 4800, Radeon 9700 Pro, Radeon 9800 XT, Radeon X850 XT Platinum Edition, Radeon X1950 XTX, GeForce 6800 Ultra, GeForce 7800 GTX 512, GeForce 8800 Ultra.
- Dual-GPU flagships (GeForce 7950 GX2, 9800 GX2, GTX 295, Radeon HD 3870 X2, HD 4870 X2, HD 5970, GTX 590, HD 6990, GTX 690, R9 295X2, Titan Z), Titan V and Titan RTX.
- Any card from before about 2008 boxed and complete as new.

"uncommon" is a collectable card that turns up now and then: 3dfx's Voodoo Graphics, Voodoo2 and Voodoo3 cards, other early cards' top models (Riva TNT2 Ultra, GeForce2 GTS, GeForce4 Ti 4200, Radeon 9800 Pro, GeForce 6800 GT), GeForce 7900 GTX, GeForce 8800 GTX, GTX 280 and 285, the GTX Titan, Titan Black, Titan X and Titan Xp, Radeon R9 Fury X and Radeon VII.

"common" is everything else, which is nearly every listing: the high-end gaming cards that sold in large numbers (GTX 480, 580, 780 Ti, 980 Ti, 1080 Ti, RTX 2080, 2080 Super and 2080 Ti, Radeon HD 4870, 5870, 7970, R9 290X), mainstream and budget cards, OEM and workstation cards (Quadro, FirePro), laptop parts, anything newer than the RTX 2080's generation (RTX 30 series on, Radeon RX 6000 on), coolers, boxes and parts, and cards sold for parts or not working. If you're unsure, say "common".`

// gpuDealInstruction asks whether graphics cards' asking prices are fair.
const gpuDealInstruction = `You judge eBay listings of graphics cards for a collector in the UK: is the asking price (postage included) a good deal, fair, or overpriced, against what such cards actually sell for on eBay UK today (not what sellers ask)? Go by the card as described (tested and working, boxed, untested, for parts), working and used if it doesn't say.

For each give:
- verdict: "good" if it's clearly below what it sells for, "overpriced" if clearly above, else "fair".
- low, high: the fair range in whole pounds, postage included.
- why: a few words, e.g. "sells for £60-90 working".`
